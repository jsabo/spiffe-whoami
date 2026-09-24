package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// AWSResult is what the page shows for the "using it: AWS" section.
type AWSResult struct {
	Configured  bool   `json:"configured"`
	RoleARN     string `json:"role_arn,omitempty"`
	CallerARN   string `json:"caller_arn,omitempty"`
	Account     string `json:"account,omitempty"`
	SecretID    string `json:"secret_id,omitempty"`
	SecretValue string `json:"secret_value_masked,omitempty"`
	Error       string `json:"error,omitempty"`
	Elapsed     string `json:"elapsed,omitempty"`
	Cached      string `json:"cached_for,omitempty"` // set when served from the 30 s cache
}

// jwtTokenRetriever hands the AWS SDK a fresh JWT SVID every time it needs to
// assume the role. The token never touches disk: the SDK calls GetIdentityToken,
// we fetch a 15-minute JWT from the Workload API with audience sts.amazonaws.com,
// and STS exchanges it for role credentials because the AWS OIDC provider trusts
// the Teleport cluster's issuer and the role's trust policy names this SPIFFE ID.
type jwtTokenRetriever struct {
	id *Identity
}

func (r jwtTokenRetriever) GetIdentityToken() ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	svid, err := r.id.JWT(ctx, "sts.amazonaws.com")
	if err != nil {
		return nil, err
	}
	return []byte(svid.Marshal()), nil
}

// awsCache keeps the last result for a short time so refreshing the page during
// a demo does not pay the STS round trip every time. The identity itself is
// not cached; the SDK re-exchanges the JWT when the role credentials expire.
var awsCache struct {
	mu     sync.Mutex
	result *AWSResult
	at     time.Time
}

const awsCacheTTL = 30 * time.Second

func callAWS(ctx context.Context, cfg Config, id *Identity) *AWSResult {
	if cfg.AWSRoleARN == "" {
		return &AWSResult{Configured: false}
	}
	awsCache.mu.Lock()
	if awsCache.result != nil && time.Since(awsCache.at) < awsCacheTTL {
		r := *awsCache.result
		r.Cached = time.Since(awsCache.at).Round(time.Second).String()
		awsCache.mu.Unlock()
		return &r
	}
	awsCache.mu.Unlock()

	res := doCallAWS(ctx, cfg, id)
	awsCache.mu.Lock()
	awsCache.result, awsCache.at = res, time.Now()
	awsCache.mu.Unlock()
	return res
}

func doCallAWS(ctx context.Context, cfg Config, id *Identity) *AWSResult {
	start := time.Now()
	res := &AWSResult{Configured: true, RoleARN: cfg.AWSRoleARN, SecretID: cfg.AWSSecretID}

	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	// Anonymous STS client for the exchange; the provider signs nothing itself.
	stsClient := sts.New(sts.Options{Region: cfg.AWSRegion, Credentials: aws.AnonymousCredentials{}})
	provider := stscreds.NewWebIdentityRoleProvider(stsClient, cfg.AWSRoleARN, jwtTokenRetriever{id: id},
		func(o *stscreds.WebIdentityRoleOptions) { o.RoleSessionName = sessionName(id) })

	awsCfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(cfg.AWSRegion),
		config.WithCredentialsProvider(aws.NewCredentialsCache(provider)),
	)
	if err != nil {
		res.Error = err.Error()
		return res
	}

	who, err := sts.NewFromConfig(awsCfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		res.Error = shortAWSError(err)
		res.Elapsed = time.Since(start).Round(time.Millisecond).String()
		return res
	}
	res.CallerARN = aws.ToString(who.Arn)
	res.Account = aws.ToString(who.Account)

	if cfg.AWSSecretID != "" {
		out, err := secretsmanager.NewFromConfig(awsCfg).GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{
			SecretId: aws.String(cfg.AWSSecretID),
		})
		if err != nil {
			res.Error = shortAWSError(err)
		} else {
			res.SecretValue = mask(aws.ToString(out.SecretString))
		}
	}
	res.Elapsed = time.Since(start).Round(time.Millisecond).String()
	return res
}

// sessionName makes the assumed-role ARN carry the SPIFFE ID's path, so the
// CloudTrail entry reads .../assumed-role/<role>/svc-payments-processor.
func sessionName(id *Identity) string {
	p := strings.Trim(id.SPIFFEID().Path(), "/")
	p = strings.NewReplacer("/", "-", ":", "-").Replace(p)
	if len(p) > 64 {
		p = p[:64]
	}
	if p == "" {
		return "spiffe-whoami"
	}
	return p
}

func mask(s string) string {
	if len(s) <= 4 {
		return strings.Repeat("*", len(s))
	}
	return s[:2] + strings.Repeat("*", len(s)-4) + s[len(s)-2:]
}

// shortAWSError keeps the code and message, drops the request-ID noise.
func shortAWSError(err error) string {
	s := err.Error()
	if i := strings.Index(s, ", RequestID:"); i > 0 {
		s = s[:i]
	}
	return fmt.Sprintf("%s", s)
}
