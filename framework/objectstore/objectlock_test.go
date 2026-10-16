package objectstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// TestApplyObjectLockSetsS3Retention checks a retained put carries the lock
// mode, the retain-until date and the checksum S3 requires with Object Lock.
func TestApplyObjectLockSetsS3Retention(t *testing.T) {
	until := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	input, err := buildPutObjectInput("bucket", "key", []byte("{}"), nil, false, "")
	if err != nil {
		t.Fatal(err)
	}
	applyObjectLock(input, &Retention{Mode: ObjectLockCompliance, RetainUntil: until})
	if input.ObjectLockMode != types.ObjectLockModeCompliance || !input.ObjectLockRetainUntilDate.Equal(until) || input.ChecksumAlgorithm != types.ChecksumAlgorithmSha256 {
		t.Fatalf("lock fields = mode %q until %v checksum %q", input.ObjectLockMode, input.ObjectLockRetainUntilDate, input.ChecksumAlgorithm)
	}

	plain, _ := buildPutObjectInput("bucket", "key", []byte("{}"), nil, false, "")
	applyObjectLock(plain, nil)
	if plain.ObjectLockMode != "" || plain.ObjectLockRetainUntilDate != nil {
		t.Fatal("a put without retention must not set lock fields")
	}
	governance, _ := buildPutObjectInput("bucket", "key", []byte("{}"), nil, false, "")
	applyObjectLock(governance, &Retention{Mode: ObjectLockGovernance, RetainUntil: until})
	if governance.ObjectLockMode != types.ObjectLockModeGovernance {
		t.Fatalf("governance mode = %q", governance.ObjectLockMode)
	}
}

// TestInMemoryStoreHonoursRetention checks the test store refuses to replace or
// delete a locked object, so callers' WORM handling is exercised in tests.
func TestInMemoryStoreHonoursRetention(t *testing.T) {
	ctx := context.Background()
	store := NewInMemoryObjectStore()
	lock := PutOptions{Retention: &Retention{Mode: ObjectLockCompliance, RetainUntil: time.Now().Add(time.Hour)}}
	if err := store.PutWithOptions(ctx, "k", []byte("a"), nil, lock); err != nil {
		t.Fatal(err)
	}
	if err := store.Put(ctx, "k", []byte("b"), nil); !errors.Is(err, ErrObjectLocked) {
		t.Fatalf("overwrite err = %v, want ErrObjectLocked", err)
	}
	if err := store.Delete(ctx, "k"); !errors.Is(err, ErrObjectLocked) {
		t.Fatalf("delete err = %v, want ErrObjectLocked", err)
	}
	if got, _ := store.Get(ctx, "k"); string(got) != "a" {
		t.Fatalf("locked object changed to %q", got)
	}
}
