// Copyright 2023 Hedgehog
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cnc

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
	"net"
	"time"

	"github.com/pkg/errors"
)

const (
	BlockTypeCert = "CERTIFICATE"
	BlockTypeKey  = "EC PRIVATE KEY"
)

type KeyPair struct {
	Cert string `json:"cert,omitempty"`
	Key  string `json:"key,omitempty"`
}

func (kp *KeyPair) PCert() (*x509.Certificate, error) {
	block, _ := pem.Decode([]byte(kp.Cert))
	if block == nil {
		return nil, errors.New("failed to parse certificate PEM")
	}

	if block.Type != BlockTypeCert {
		return nil, errors.Errorf("invalid block type '%s' while expected '%s'", block.Type, BlockTypeCert)
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, errors.Wrap(err, "failed to parse certificate")
	}

	return cert, nil
}

func (kp *KeyPair) PKey() (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(kp.Key))
	if block == nil {
		return nil, errors.New("failed to parse certificate PEM")
	}

	if block.Type != BlockTypeKey {
		return nil, errors.Errorf("invalid block type '%s' while expected '%s'", block.Type, BlockTypeKey)
	}

	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.Wrap(err, "failed to parse private key")
	}

	return key, nil
}

func (kp *KeyPair) Ensure(cn string, parent *KeyPair, keyUsage x509.KeyUsage, extKeyUsage []x509.ExtKeyUsage, ips []string, dnsNames []string) error {
	if kp.Cert != "" && kp.Key != "" {
		_, err := kp.PCert()
		if err != nil {
			return errors.Wrap(err, "error parsing existing certificate")
		}
		_, err = kp.PKey()
		if err != nil {
			return errors.Wrap(err, "error parsing existing private key")
		}

		// TODO maybe do a bit more checks?

		return nil
	}

	isCA := parent == nil

	years := 1
	if isCA {
		years = 10
	}

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(mathrand.Int63()), //nolint:gosec
		Subject: pkix.Name{
			Country:      []string{"US"},
			Province:     []string{"Washington"},
			Locality:     []string{"Seattle"},
			Organization: []string{"Hedgehog SONiC Foundation"},
			CommonName:   cn,
		},
		NotBefore:             time.Now().Add(-15 * time.Minute),
		NotAfter:              time.Now().AddDate(years, 0, 0),
		IsCA:                  isCA,
		ExtKeyUsage:           extKeyUsage,
		KeyUsage:              keyUsage,
		BasicConstraintsValid: isCA,
		// TODO SubjectKeyId and AuthorityKeyId
	}

	if isCA && (len(ips) > 0 || len(dnsNames) > 0) {
		return errors.Errorf("CA certificate cannot have IP addresses or DNS names, cn: %s", cn)
	}

	if !isCA {
		tmpl.DNSNames = dnsNames

		for _, ip := range ips {
			addr := net.ParseIP(ip)
			if addr == nil {
				return errors.Errorf("invalid IP address '%s'", ip)
			}
			tmpl.IPAddresses = append(tmpl.IPAddresses, addr)
		}
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), cryptorand.Reader)
	if err != nil {
		return errors.Wrapf(err, "error generating private key")
	}

	parentCert := tmpl
	parentKey := key
	if parent != nil {
		parentCert, err = parent.PCert()
		if err != nil {
			return errors.Wrapf(err, "error parsing parent certificate")
		}
		parentKey, err = parent.PKey()
		if err != nil {
			return errors.Wrapf(err, "error parsing parent private key")
		}
	}
	cert, err := x509.CreateCertificate(cryptorand.Reader, tmpl, parentCert, &key.PublicKey, parentKey)
	if err != nil {
		return errors.Wrapf(err, "error creating certificate")
	}

	certPem := new(bytes.Buffer)
	err = pem.Encode(certPem, &pem.Block{
		Type:  BlockTypeCert,
		Bytes: cert,
	})
	if err != nil {
		return errors.Wrapf(err, "error encoding certificate")
	}

	keyPem := new(bytes.Buffer)
	keyBytes, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return errors.Wrapf(err, "error encoding private key")
	}
	err = pem.Encode(keyPem, &pem.Block{
		Type:  BlockTypeKey,
		Bytes: keyBytes,
	})
	if err != nil {
		return errors.Wrapf(err, "error encoding private key")
	}

	kp.Cert = certPem.String()
	kp.Key = keyPem.String()

	return nil
}
