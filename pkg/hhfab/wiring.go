package hhfab

import (
	"context"
	"fmt"
	"io"
	"reflect"

	"go.githedgehog.com/fabric/api/meta"
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1alpha2"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1alpha2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

type printObj struct {
	APIVersion string       `json:"apiVersion,omitempty"`
	Kind       string       `json:"kind,omitempty"`
	Meta       printObjMeta `json:"metadata,omitempty"`
	Spec       any          `json:"spec,omitempty"`
}

type printObjMeta struct {
	Name        string            `json:"name,omitempty"`
	Namespace   string            `json:"namespace,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

func printWiring(ctx context.Context, kube client.Reader, w io.Writer) error {
	objs := 0

	if err := printObjectList(ctx, kube, w, &wiringapi.VLANNamespaceList{}, &objs); err != nil {
		return fmt.Errorf("printing vlan namespaces: %w", err)
	}

	if err := printObjectList(ctx, kube, w, &vpcapi.IPv4NamespaceList{}, &objs); err != nil {
		return fmt.Errorf("printing ipv4 namespaces: %w", err)
	}

	if err := printObjectList(ctx, kube, w, &wiringapi.SwitchGroupList{}, &objs); err != nil {
		return fmt.Errorf("printing switch groups: %w", err)
	}

	if err := printObjectList(ctx, kube, w, &wiringapi.SwitchList{}, &objs); err != nil {
		return fmt.Errorf("printing switches: %w", err)
	}

	if err := printObjectList(ctx, kube, w, &wiringapi.ServerList{}, &objs); err != nil {
		return fmt.Errorf("printing servers: %w", err)
	}

	if err := printObjectList(ctx, kube, w, &wiringapi.ConnectionList{}, &objs); err != nil {
		return fmt.Errorf("printing connections: %w", err)
	}

	if err := printObjectList(ctx, kube, w, &vpcapi.ExternalList{}, &objs); err != nil {
		return fmt.Errorf("printing externals: %w", err)
	}

	if err := printObjectList(ctx, kube, w, &vpcapi.ExternalAttachmentList{}, &objs); err != nil {
		return fmt.Errorf("printing external attachments: %w", err)
	}

	if err := printObjectList(ctx, kube, w, &vpcapi.VPCList{}, &objs); err != nil {
		return fmt.Errorf("printing vpcs: %w", err)
	}

	if err := printObjectList(ctx, kube, w, &vpcapi.VPCAttachmentList{}, &objs); err != nil {
		return fmt.Errorf("printing vpc attachments: %w", err)
	}

	if err := printObjectList(ctx, kube, w, &vpcapi.VPCPeeringList{}, &objs); err != nil {
		return fmt.Errorf("printing vpc peerings: %w", err)
	}

	if err := printObjectList(ctx, kube, w, &vpcapi.ExternalPeeringList{}, &objs); err != nil {
		return fmt.Errorf("printing external peerings: %w", err)
	}

	return nil
}

func printObjectList(ctx context.Context, kube client.Reader, w io.Writer, objList meta.ObjectList, objs *int) error {
	if objs == nil {
		return fmt.Errorf("counter is nil") //nolint:goerr113
	}

	if err := kube.List(ctx, objList); err != nil {
		return fmt.Errorf("listing objects: %w", err)
	}

	if len(objList.GetItems()) > 0 {
		_, err := fmt.Fprintf(w, "#\n# %s\n#\n", reflect.TypeOf(objList).Elem().Name())
		if err != nil {
			return fmt.Errorf("writing comment: %w", err)
		}
	}

	for _, obj := range objList.GetItems() {
		if *objs > 0 {
			_, err := fmt.Fprintf(w, "---\n")
			if err != nil {
				return fmt.Errorf("writing separator: %w", err)
			}
		}
		*objs++

		if err := printObject(obj, w); err != nil {
			return fmt.Errorf("printing object: %w", err)
		}
	}

	return nil
}

func printObject(obj client.Object, w io.Writer) error {
	ns := obj.GetNamespace()
	if ns == metav1.NamespaceDefault {
		ns = ""
	}

	spec := reflect.ValueOf(obj).Elem().FieldByName("Spec").Interface()

	p := printObj{
		APIVersion: obj.GetObjectKind().GroupVersionKind().GroupVersion().String(),
		Kind:       obj.GetObjectKind().GroupVersionKind().Kind,
		Meta: printObjMeta{
			Name:        obj.GetName(),
			Namespace:   ns,
			Labels:      obj.GetLabels(),
			Annotations: obj.GetAnnotations(),
		},
		Spec: spec,
	}

	buf, err := yaml.Marshal(p)
	if err != nil {
		return fmt.Errorf("marshalling: %w", err)
	}
	_, err = w.Write(buf)
	if err != nil {
		return fmt.Errorf("writing: %w", err)
	}

	return nil
}
