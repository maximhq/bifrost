package chaosmock

import (
	"encoding/xml"
	"net/http"
	"strings"
	"sync"
	"time"
)

// The fleet's fake AWS account: an EKS Pod Identity credentials endpoint and STS,
// so a process pointed at them (AWS_CONTAINER_CREDENTIALS_FULL_URI and
// AWS_ENDPOINT_URL_STS) gets credentials the SigV4 servers can verify, the way
// Bifrost's pods do in EKS:
//
//	GET  /aws/credentials  the pod's credentials (needs the container authorization token)
//	POST /aws/sts          STS AssumeRole, itself SigV4-signed by the pod for sts
//	GET  /aws/state        counters and the AssumeRole log
//	POST /aws/reset        zero them
//
// Trust policies: a role whose name contains "external" needs ExternalId
// AWSExternalID, and a role whose name contains "denied" cannot be assumed.
const (
	AWSPodAccessKeyID   = "AKIDCHAOSPOD"
	AWSPodIdentity      = "pod"
	AWSContainerToken   = "chaos-pod-identity"
	AWSExternalID       = "chaos-external-id"
	awsCredentialLife   = time.Hour
	awsSecretDerivation = "chaosmock-aws-secret|"
)

// AWSAssumeRoleEntry is one AssumeRole call STS answered.
type AWSAssumeRoleEntry struct {
	RoleARN     string `json:"role_arn"`
	SessionName string `json:"session_name"`
	ExternalID  string `json:"external_id,omitempty"`
	Caller      string `json:"caller,omitempty"`
	// Result is "ok" or why the call was refused.
	Result string `json:"result"`
}

// AWSState is the fake account's counters and log.
type AWSState struct {
	CredentialFetches int64                `json:"credential_fetches"`
	AssumeRole        []AWSAssumeRoleEntry `json:"assume_role"`
}

// fakeAWSAccount knows every access key it issued.
type fakeAWSAccount struct {
	mu         sync.Mutex
	identities map[string]string // access key ID -> identity
	d          AWSState
}

// awsAccount is shared by every fleet in the process: its keys derive from their
// identity, so two fleets issuing the same role session agree.
var awsAccount = &fakeAWSAccount{identities: map[string]string{AWSPodAccessKeyID: AWSPodIdentity}}

// lookup returns a known access key's secret, identity and session token. Every
// key this account issues is temporary.
func (a *fakeAWSAccount) lookup(accessKeyID string) (string, string, string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	identity, ok := a.identities[accessKeyID]
	return awsSecretFor(accessKeyID), identity, awsTokenFor(accessKeyID), ok
}

// awsSecretFor derives an access key's secret, so no secret needs storing.
func awsSecretFor(accessKeyID string) string {
	return hexSHA256([]byte(awsSecretDerivation + accessKeyID))
}

// awsTokenFor derives the session token issued with an access key.
func awsTokenFor(accessKeyID string) string {
	return "chaos-session-" + accessKeyID
}

// routeAWS serves the fake account's endpoints.
func (a *fakeAWSAccount) routeAWS(w http.ResponseWriter, r *http.Request) {
	switch path := strings.TrimPrefix(r.URL.Path, "/aws/"); {
	case path == "credentials" && r.Method == http.MethodGet:
		a.serveCredentials(w, r)
	case path == "sts" && r.Method == http.MethodPost:
		a.serveSTS(w, r)
	case path == "state" && r.Method == http.MethodGet:
		a.mu.Lock()
		d := a.d
		d.AssumeRole = append([]AWSAssumeRoleEntry{}, a.d.AssumeRole...)
		a.mu.Unlock()
		writeJSON(w, http.StatusOK, d)
	case path == "reset" && r.Method == http.MethodPost:
		a.mu.Lock()
		a.d = AWSState{}
		a.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]bool{"reset": true})
	default:
		http.NotFound(w, r)
	}
}

// serveCredentials answers as the EKS Pod Identity agent does.
func (a *fakeAWSAccount) serveCredentials(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != AWSContainerToken {
		http.Error(w, "missing or wrong container authorization token", http.StatusUnauthorized)
		return
	}
	a.mu.Lock()
	a.d.CredentialFetches++
	a.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]string{
		"AccessKeyId":     AWSPodAccessKeyID,
		"SecretAccessKey": awsSecretFor(AWSPodAccessKeyID),
		"Token":           awsTokenFor(AWSPodAccessKeyID),
		"Expiration":      time.Now().Add(awsCredentialLife).UTC().Format(time.RFC3339),
	})
}

// serveSTS answers AssumeRole for a request the pod signed for sts.
func (a *fakeAWSAccount) serveSTS(w http.ResponseWriter, r *http.Request) {
	body, err := readAndRestoreBody(r)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}
	var entry SigV4Entry
	caller, reason := checkSigV4(r, body, a.lookup, "", "sts", time.Now(), &entry)
	if err := r.ParseForm(); err != nil || r.PostForm.Get("Action") != "AssumeRole" {
		stsError(w, http.StatusBadRequest, "InvalidAction", "only AssumeRole is supported")
		return
	}
	log := AWSAssumeRoleEntry{RoleARN: r.PostForm.Get("RoleArn"), SessionName: r.PostForm.Get("RoleSessionName"), ExternalID: r.PostForm.Get("ExternalId"), Caller: caller}
	role := log.RoleARN[strings.LastIndex(log.RoleARN, "/")+1:]
	status, code := http.StatusOK, ""
	switch {
	case reason != "":
		status, code, reason = http.StatusForbidden, "SignatureDoesNotMatch", "sts request: "+reason
	case !strings.HasPrefix(log.RoleARN, "arn:") || !strings.Contains(log.RoleARN, ":role/"):
		status, code, reason = http.StatusBadRequest, "ValidationError", "RoleArn is not a role ARN"
	case strings.Contains(role, "denied"):
		status, code, reason = http.StatusForbidden, "AccessDenied", caller+" is not authorized to perform sts:AssumeRole on "+log.RoleARN
	case strings.Contains(role, "external") && log.ExternalID != AWSExternalID:
		status, code, reason = http.StatusForbidden, "AccessDenied", "the trust policy of "+log.RoleARN+" requires the external ID"
	}
	log.Result = "ok"
	if reason != "" {
		log.Result = reason
	}
	a.mu.Lock()
	a.d.AssumeRole = append(a.d.AssumeRole, log)
	a.mu.Unlock()
	if status != http.StatusOK {
		stsError(w, status, code, reason)
		return
	}

	identity := "assumed-role/" + role + "/" + log.SessionName
	accessKeyID := "ASIA" + strings.ToUpper(hexSHA256([]byte(identity))[:16])
	a.mu.Lock()
	a.identities[accessKeyID] = identity
	a.mu.Unlock()
	type credentials struct {
		AccessKeyID     string `xml:"AccessKeyId"`
		SecretAccessKey string `xml:"SecretAccessKey"`
		SessionToken    string `xml:"SessionToken"`
		Expiration      string `xml:"Expiration"`
	}
	resp := struct {
		XMLName     xml.Name    `xml:"https://sts.amazonaws.com/doc/2011-06-15/ AssumeRoleResponse"`
		Credentials credentials `xml:"AssumeRoleResult>Credentials"`
		AssumedARN  string      `xml:"AssumeRoleResult>AssumedRoleUser>Arn"`
		AssumedID   string      `xml:"AssumeRoleResult>AssumedRoleUser>AssumedRoleId"`
		RequestID   string      `xml:"ResponseMetadata>RequestId"`
	}{
		Credentials: credentials{accessKeyID, awsSecretFor(accessKeyID), awsTokenFor(accessKeyID), time.Now().Add(awsCredentialLife).UTC().Format(time.RFC3339)},
		AssumedARN:  strings.Replace(strings.Replace(log.RoleARN, ":iam::", ":sts::", 1), ":role/", ":assumed-role/", 1) + "/" + log.SessionName,
		AssumedID:   "AROACHAOS:" + log.SessionName,
		RequestID:   randomID(),
	}
	w.Header().Set("Content-Type", "text/xml")
	_ = xml.NewEncoder(w).Encode(resp)
}

// stsError writes an STS error response.
func stsError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "text/xml")
	w.WriteHeader(status)
	_ = xml.NewEncoder(w).Encode(struct {
		XMLName xml.Name `xml:"ErrorResponse"`
		Type    string   `xml:"Error>Type"`
		Code    string   `xml:"Error>Code"`
		Message string   `xml:"Error>Message"`
	}{Type: "Sender", Code: code, Message: message})
}
