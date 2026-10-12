package sqs

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

var ErrUnauthorizedProducer = errors.New("unauthorized SQS producer")

// ProviderKeys binds a signing key to each authorized provider. SQS attributes
// alone are untrusted: HMAC authenticates the provider and exact envelope bytes.
// LocalStack does not enforce the production IAM trust boundary.
type ProviderKeys map[string]string

func (keys ProviderKeys) Validate() error {
	if len(keys) == 0 {
		return fmt.Errorf("SQS_PROVIDER_KEYS must contain at least one provider")
	}
	for provider, key := range keys {
		if strings.TrimSpace(provider) == "" || strings.ContainsRune(provider, 0) || len(key) < 32 {
			return fmt.Errorf("SQS_PROVIDER_KEYS requires nonempty provider ids and keys of at least 32 bytes")
		}
	}
	return nil
}

// SignAttributes is used by trusted producers; never derive the key from a body.
// Versioned, NUL-delimited input prevents ambiguous provider/body concatenation.
func SignAttributes(provider, key string, body []byte) map[string]types.MessageAttributeValue {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte("jungle-wager-v1\x00" + provider + "\x00"))
	mac.Write(body)
	return map[string]types.MessageAttributeValue{
		"providerId": {DataType: aws.String("String"), StringValue: aws.String(provider)},
		"signature":  {DataType: aws.String("String"), StringValue: aws.String(hex.EncodeToString(mac.Sum(nil)))},
	}
}

func (keys ProviderKeys) Authenticate(body []byte, attributes map[string]types.MessageAttributeValue) (string, error) {
	identity, signature := attributes["providerId"], attributes["signature"]
	provider := aws.ToString(identity.StringValue)
	key, known := keys[provider]
	if !known || len(key) < 32 || aws.ToString(identity.DataType) != "String" || aws.ToString(signature.DataType) != "String" {
		return "", ErrUnauthorizedProducer
	}
	supplied, err := hex.DecodeString(aws.ToString(signature.StringValue))
	if err != nil {
		return "", ErrUnauthorizedProducer
	}
	expected := SignAttributes(provider, key, body)
	expectedBytes, _ := hex.DecodeString(aws.ToString(expected["signature"].StringValue))
	if !hmac.Equal(supplied, expectedBytes) {
		return "", ErrUnauthorizedProducer
	}
	return provider, nil
}
