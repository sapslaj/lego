package tlsalpn01

import (
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509/pkix"
	"encoding/asn1"
	"fmt"
	"time"

	"github.com/go-acme/lego/v4/acme"
	"github.com/go-acme/lego/v4/acme/api"
	"github.com/go-acme/lego/v4/certcrypto"
	"github.com/go-acme/lego/v4/challenge"
	"github.com/go-acme/lego/v4/log"
)

// idPeAcmeIdentifierV1 is the SMI Security for PKIX Certification Extension OID referencing the ACME extension.
// Reference: https://www.rfc-editor.org/rfc/rfc8737.html#section-6.1
var idPeAcmeIdentifierV1 = asn1.ObjectIdentifier{1, 3, 6, 1, 5, 5, 7, 1, 31}

type ValidateFunc func(core *api.Core, domain string, chlng acme.Challenge) error

type ChallengeOption func(*Challenge) error

// SetDelay sets a delay between the start of the TLS listener and the challenge validation.
func SetDelay(delay time.Duration) ChallengeOption {
	return func(chlg *Challenge) error {
		chlg.delay = delay
		return nil
	}
}

type Challenge struct {
	core     *api.Core
	validate ValidateFunc
	provider challenge.Provider
	delay    time.Duration
}

func NewChallenge(core *api.Core, validate ValidateFunc, provider challenge.Provider, opts ...ChallengeOption) *Challenge {
	chlg := &Challenge{
		core:     core,
		validate: validate,
		provider: provider,
	}

	for _, opt := range opts {
		err := opt(chlg)
		if err != nil {
			log.Infof("challenge option error: %v", err)
		}
	}

	return chlg
}

func (c *Challenge) SetProvider(provider challenge.Provider) {
	c.provider = provider
}

// Solve manages the provider to validate and solve the challenge.
func (c *Challenge) Solve(authz acme.Authorization) error {
	domain := authz.Identifier.Value
	log.Infof("[%s] acme: Trying to solve TLS-ALPN-01", challenge.GetTargetedDomain(authz))

	chlng, err := challenge.FindChallenge(challenge.TLSALPN01, authz)
	if err != nil {
		return err
	}

	// Generate the Key Authorization for the challenge
	keyAuth, err := c.core.GetKeyAuthorization(chlng.Token)
	if err != nil {
		return err
	}

	err = c.provider.Present(domain, chlng.Token, keyAuth)
	if err != nil {
		return fmt.Errorf("[%s] acme: error presenting token: %w", challenge.GetTargetedDomain(authz), err)
	}
	defer func() {
		err := c.provider.CleanUp(domain, chlng.Token, keyAuth)
		if err != nil {
			log.Warnf("[%s] acme: cleaning up failed: %v", challenge.GetTargetedDomain(authz), err)
		}
	}()

	if c.delay > 0 {
		time.Sleep(c.delay)
	}

	chlng.KeyAuthorization = keyAuth
	return c.validate(c.core, domain, chlng)
}

// ChallengeBlocks returns PEM blocks (certPEMBlock, keyPEMBlock) with the acmeValidation-v1 extension
// and domain name for the `tls-alpn-01` challenge.
func ChallengeBlocks(domain, keyAuth string) ([]byte, []byte, error) {
	// Compute the SHA-256 digest of the key authorization.
	zBytes := sha256.Sum256([]byte(keyAuth))

	value, err := asn1.Marshal(zBytes[:sha256.Size])
	if err != nil {
		return nil, nil, err
	}

	// Add the keyAuth digest as the acmeValidation-v1 extension
	// (marked as critical such that it won't be used by non-ACME software).
	// Reference: https://www.rfc-editor.org/rfc/rfc8737.html#section-3
	extensions := []pkix.Extension{
		{
			Id:       idPeAcmeIdentifierV1,
			Critical: true,
			Value:    value,
		},
	}

	// Generate a new RSA key for the certificates.
	tempPrivateKey, err := certcrypto.GeneratePrivateKey(certcrypto.RSA2048)
	if err != nil {
		return nil, nil, err
	}

	rsaPrivateKey := tempPrivateKey.(*rsa.PrivateKey)

	// Generate the PEM certificate using the provided private key, domain, and extra extensions.
	tempCertPEM, err := certcrypto.GeneratePemCert(rsaPrivateKey, domain, extensions)
	if err != nil {
		return nil, nil, err
	}

	// Encode the private key into a PEM format. We'll need to use it to generate the x509 keypair.
	rsaPrivatePEM := certcrypto.PEMEncode(rsaPrivateKey)

	return tempCertPEM, rsaPrivatePEM, nil
}

// ChallengeCert returns a certificate with the acmeValidation-v1 extension
// and domain name for the `tls-alpn-01` challenge.
func ChallengeCert(domain, keyAuth string) (*tls.Certificate, error) {
	tempCertPEM, rsaPrivatePEM, err := ChallengeBlocks(domain, keyAuth)
	if err != nil {
		return nil, err
	}

	cert, err := tls.X509KeyPair(tempCertPEM, rsaPrivatePEM)
	if err != nil {
		return nil, err
	}

	return &cert, nil
}
