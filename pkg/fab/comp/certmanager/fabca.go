package certmanager

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	cryptorand "crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	mathrand "math/rand"
	"time"

	"github.com/pkg/errors"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	BlockTypeCert = "CERTIFICATE"
	BlockTypeKey  = "EC PRIVATE KEY"
)

func InstallFabCA(ca *CA) comp.KubeInstall {
	return func(_ fabapi.Fabricator) ([]client.Object, error) {
		return []client.Object{
			comp.NewSecret(comp.FabCASecret, map[string]string{
				"tls.crt": ca.Crt, // TODO const for keys?
				"tls.key": ca.Key,
			}),
			comp.NewConfigMap(comp.FabCAConfigMap, map[string]string{
				"ca.crt": ca.Crt, // TODO const for keys?
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
		return nil, errors.Wrapf(err, "error generating private key")
	}

	cert, err := x509.CreateCertificate(cryptorand.Reader, crt, crt, &key.PublicKey, key)
	if err != nil {
		return nil, errors.Wrapf(err, "error creating certificate")
	}

	certPem := new(bytes.Buffer)
	err = pem.Encode(certPem, &pem.Block{
		Type:  BlockTypeCert,
		Bytes: cert,
	})
	if err != nil {
		return nil, errors.Wrapf(err, "error encoding certificate")
	}

	keyPem := new(bytes.Buffer)
	keyBytes, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, errors.Wrapf(err, "error encoding private key")
	}
	err = pem.Encode(keyPem, &pem.Block{
		Type:  BlockTypeKey,
		Bytes: keyBytes,
	})
	if err != nil {
		return nil, errors.Wrapf(err, "error encoding private key")
	}

	return &CA{
		Crt: certPem.String(),
		Key: keyPem.String(),
	}, nil
}
