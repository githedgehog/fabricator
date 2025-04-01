// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package certmanager

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	cryptorand "crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	mathrand "math/rand"
	"time"

	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	BlockTypeCert = "CERTIFICATE"
	BlockTypeKey  = "EC PRIVATE KEY"
	FabCAPath     = "/etc/ssl/certs/hh-fab-ca.pem"
)

func InstallFabCA(ca *CA) comp.KubeInstall {
	return func(_ fabapi.Fabricator) ([]kclient.Object, error) {
		return []kclient.Object{
			comp.NewSecret(comp.FabCASecret, comp.SecretTypeOpaque, map[string]string{
				"tls.crt": ca.Crt,
				"tls.key": ca.Key,
			}),
			comp.NewConfigMap(comp.FabCAConfigMap, map[string]string{
				comp.FabCAConfigMapKey: ca.Crt, // changing key will break fabric manager
			}),
			comp.NewIssuer(comp.FabCAIssuer, comp.IssuerSpec{
				IssuerConfig: comp.IssuerConfig{
					CA: &comp.CAIssuer{
						SecretName: comp.FabCASecret,
					},
				},
			}),
		}, nil
	}
}

type CA struct {
	Crt string `json:"crt,omitempty"`
	Key string `json:"key,omitempty"`
}

func NewFabCA() (*CA, error) {
	crt := &x509.Certificate{
		SerialNumber: big.NewInt(mathrand.Int63()), //nolint:gosec
		Subject: pkix.Name{
			CommonName: "hedgehog-fab-ca",
		},
		NotBefore:             time.Now().Add(-15 * time.Minute),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), cryptorand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generating private key: %w", err)
	}

	cert, err := x509.CreateCertificate(cryptorand.Reader, crt, crt, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("creating certificate: %w", err)
	}

	certPem := new(bytes.Buffer)
	err = pem.Encode(certPem, &pem.Block{
		Type:  BlockTypeCert,
		Bytes: cert,
	})
	if err != nil {
		return nil, fmt.Errorf("encoding certificate: %w", err)
	}

	keyPem := new(bytes.Buffer)
	keyBytes, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("encoding private key: %w", err)
	}
	err = pem.Encode(keyPem, &pem.Block{
		Type:  BlockTypeKey,
		Bytes: keyBytes,
	})
	if err != nil {
		return nil, fmt.Errorf("encoding private key: %w", err)
	}

	return &CA{
		Crt: certPem.String(),
		Key: keyPem.String(),
	}, nil
}
