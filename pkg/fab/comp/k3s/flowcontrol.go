// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package k3s

import (
	agentapi "go.githedgehog.com/fabric/api/agent/v1beta1"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
	coordinationapi "k8s.io/api/coordination/v1"
	flowcontrolapi "k8s.io/api/flowcontrol/v1"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// The apiserver's priority and fairness configuration, so that the Fabric
// controllers, the switch agents, and Kubernetes itself cannot starve each
// other.
//
// Stock Kubernetes sends every ServiceAccount outside kube-system to the one
// workload-low priority level. On a Fabric that is fabric-ctrl's lease renewals
// and reconciles together with every agent's status writes, so a busy fleet can
// delay a renewal past its deadline and make the controller lose leadership and
// restart - which then re-lists everything and adds to the load.
//
// The FlowSchemas here sit between the stock ones (precedence up to 900) and the
// catch-all service-accounts FlowSchema (9000), so nothing the stock FlowSchemas
// match changes:
//
//   - 1000: leader-election leases of every ServiceAccount in the fab namespace
//     go to the stock leader-election level, ahead of everything else they do
//   - 1100: everything else those ServiceAccounts do goes to hh-control, which
//     keeps half its seats however busy anyone else is. That is the Fabric and
//     Fabricator controllers and the services they run, and also the gateway
//     agents, which are few
//   - 1200: switch agents go to hh-agents, which queues fairly per agent and can
//     borrow as many seats again as it has from whatever other levels are not
//     using theirs, so a busy fleet uses idle capacity but gives it back as
//     soon as its owner needs it
//
// Adding levels splits the apiserver's total concurrency more ways, so the
// totals in the server config are raised to keep every stock level at the seats
// it has on a stock cluster (see TotalSeats).
//
// Users authenticating as system:masters, such as the admin kubeconfig, match
// the stock exempt schema first and are never limited by any of this.
const (
	PriorityControl        = "hh-control"
	PriorityAgents         = "hh-agents"
	priorityLeaderElection = "leader-election" // stock, used by the built-in controllers' leases

	SchemaLeaderElection = "hh-leader-election"
	SchemaControl        = "hh-control"
	SchemaAgents         = "hh-agents"

	precedenceLeaderElection = 1000
	precedenceControl        = 1100
	precedenceAgents         = 1200
)

// Shares of the apiserver's total concurrency. A level gets its shares over the
// sum of every level's shares, times the total seats.
const (
	// SharesControl matches the stock workload-high level the built-in
	// controllers use.
	SharesControl = 40
	// SharesAgents matches the stock workload-low level, where agents would
	// otherwise land.
	SharesAgents = 100

	// stockShares is the sum of the stock limited levels' shares: catch-all 5,
	// global-default 20, leader-election 10, node-high 40, system 30,
	// workload-high 40 and workload-low 100.
	stockShares = 245
	// stockSeats is the stock total: max-requests-inflight 400 plus
	// max-mutating-requests-inflight 200.
	stockSeats = 600
)

// TotalSeats is the apiserver concurrency that keeps every stock level at its
// stock seats with ours added: the stock seats per share times the stock shares
// plus ours, rounded up to the next 50.
const TotalSeats = ((stockSeats*(stockShares+SharesControl+SharesAgents)+stockShares-1)/stockShares + 49) / 50 * 50

// MaxRequestsInflight and MaxMutatingRequestsInflight split TotalSeats two to
// one like the stock defaults; with priority and fairness enabled only their
// sum matters.
const (
	MaxMutatingRequestsInflight = TotalSeats / 3
	MaxRequestsInflight         = TotalSeats - MaxMutatingRequestsInflight
)

var _ comp.KubeInstall = InstallFlowControl

func InstallFlowControl(_ fabapi.Fabricator) ([]kclient.Object, error) {
	// Everything the control plane runs uses a ServiceAccount in the fab
	// namespace, so match the namespace rather than list each one.
	controllers := []flowcontrolapi.Subject{serviceAccount(comp.FabNamespace, flowcontrolapi.NameAll)}

	byUser := &flowcontrolapi.FlowDistinguisherMethod{Type: flowcontrolapi.FlowDistinguisherMethodByUserType}

	return []kclient.Object{
		&flowcontrolapi.PriorityLevelConfiguration{
			ObjectMeta: kmetav1.ObjectMeta{Name: PriorityControl},
			Spec: flowcontrolapi.PriorityLevelConfigurationSpec{
				Type: flowcontrolapi.PriorityLevelEnablementLimited,
				Limited: &flowcontrolapi.LimitedPriorityLevelConfiguration{
					NominalConcurrencyShares: new(int32(SharesControl)),
					// Half can be lent while the controllers are quiet; the
					// other half is theirs however busy everyone else is. Their
					// lease renewals do not depend on it, as they are in the
					// stock leader-election level, which lends nothing.
					LendablePercent: new(int32(50)),
					LimitResponse:   queue(64, 6, 50),
				},
			},
		},
		&flowcontrolapi.PriorityLevelConfiguration{
			ObjectMeta: kmetav1.ObjectMeta{Name: PriorityAgents},
			Spec: flowcontrolapi.PriorityLevelConfigurationSpec{
				Type: flowcontrolapi.PriorityLevelEnablementLimited,
				Limited: &flowcontrolapi.LimitedPriorityLevelConfiguration{
					NominalConcurrencyShares: new(int32(SharesAgents)),
					// Half can be lent while the fleet is quiet, and a busy
					// fleet can borrow up to its own size again from levels
					// that are idle. Borrowed seats are only ever the lenders'
					// lendable ones and go back as soon as they need them.
					LendablePercent:       new(int32(50)),
					BorrowingLimitPercent: new(int32(100)),
					LimitResponse:         queue(128, 6, 50),
				},
			},
		},
		&flowcontrolapi.FlowSchema{
			ObjectMeta: kmetav1.ObjectMeta{Name: SchemaLeaderElection},
			Spec: flowcontrolapi.FlowSchemaSpec{
				PriorityLevelConfiguration: flowcontrolapi.PriorityLevelConfigurationReference{Name: priorityLeaderElection},
				MatchingPrecedence:         precedenceLeaderElection,
				DistinguisherMethod:        byUser,
				Rules: []flowcontrolapi.PolicyRulesWithSubjects{{
					Subjects: controllers,
					ResourceRules: []flowcontrolapi.ResourcePolicyRule{{
						Verbs:      []string{"get", "create", "update"},
						APIGroups:  []string{coordinationapi.GroupName},
						Resources:  []string{"leases"},
						Namespaces: []string{flowcontrolapi.NamespaceEvery},
					}},
				}},
			},
		},
		&flowcontrolapi.FlowSchema{
			ObjectMeta: kmetav1.ObjectMeta{Name: SchemaControl},
			Spec: flowcontrolapi.FlowSchemaSpec{
				PriorityLevelConfiguration: flowcontrolapi.PriorityLevelConfigurationReference{Name: PriorityControl},
				MatchingPrecedence:         precedenceControl,
				DistinguisherMethod:        byUser,
				Rules: []flowcontrolapi.PolicyRulesWithSubjects{{
					Subjects: controllers,
					ResourceRules: []flowcontrolapi.ResourcePolicyRule{{
						Verbs:        []string{flowcontrolapi.VerbAll},
						APIGroups:    []string{flowcontrolapi.APIGroupAll},
						Resources:    []string{flowcontrolapi.ResourceAll},
						ClusterScope: true,
						Namespaces:   []string{flowcontrolapi.NamespaceEvery},
					}},
					NonResourceRules: []flowcontrolapi.NonResourcePolicyRule{{
						Verbs:           []string{flowcontrolapi.VerbAll},
						NonResourceURLs: []string{flowcontrolapi.NonResourceAll},
					}},
				}},
			},
		},
		&flowcontrolapi.FlowSchema{
			ObjectMeta: kmetav1.ObjectMeta{Name: SchemaAgents},
			Spec: flowcontrolapi.FlowSchemaSpec{
				PriorityLevelConfiguration: flowcontrolapi.PriorityLevelConfigurationReference{Name: PriorityAgents},
				MatchingPrecedence:         precedenceAgents,
				// Every agent is its own ServiceAccount, so this queues them
				// fairly against each other.
				DistinguisherMethod: byUser,
				// One ServiceAccount per switch in the default namespace,
				// matched by what they touch so that any other ServiceAccount
				// there is left to the stock FlowSchemas.
				Rules: []flowcontrolapi.PolicyRulesWithSubjects{{
					Subjects: []flowcontrolapi.Subject{serviceAccount(kmetav1.NamespaceDefault, flowcontrolapi.NameAll)},
					ResourceRules: []flowcontrolapi.ResourcePolicyRule{{
						Verbs:      []string{flowcontrolapi.VerbAll},
						APIGroups:  []string{agentapi.GroupVersion.Group},
						Resources:  []string{"agents", "agents/status"},
						Namespaces: []string{kmetav1.NamespaceDefault},
					}},
				}},
			},
		},
	}, nil
}

func serviceAccount(namespace, name string) flowcontrolapi.Subject {
	return flowcontrolapi.Subject{
		Kind:           flowcontrolapi.SubjectKindServiceAccount,
		ServiceAccount: &flowcontrolapi.ServiceAccountSubject{Namespace: namespace, Name: name},
	}
}

func queue(queues, handSize, queueLength int32) flowcontrolapi.LimitResponse {
	return flowcontrolapi.LimitResponse{
		Type: flowcontrolapi.LimitResponseTypeQueue,
		Queuing: &flowcontrolapi.QueuingConfiguration{
			Queues:           queues,
			HandSize:         handSize,
			QueueLengthLimit: queueLength,
		},
	}
}
