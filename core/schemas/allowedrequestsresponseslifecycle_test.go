package schemas

import "testing"

func TestAllowedRequests_ResponsesLifecycleGranular(t *testing.T) {
	t.Parallel()

	t.Run("nil means allow", func(t *testing.T) {
		t.Parallel()
		var ar *AllowedRequests
		if !ar.IsOperationAllowed(ResponsesRetrieveRequest) {
			t.Fatal("nil AllowedRequests should allow retrieve")
		}
	})

	t.Run("legacy responses only allows all lifecycle verbs", func(t *testing.T) {
		t.Parallel()
		ar := &AllowedRequests{Responses: true}
		for _, op := range []RequestType{
			ResponsesRetrieveRequest,
			ResponsesDeleteRequest,
			ResponsesCancelRequest,
			ResponsesInputItemsRequest,
		} {
			if !ar.IsOperationAllowed(op) {
				t.Fatalf("expected %q allowed", op)
			}
		}
	})

	t.Run("granular mode requires per-verb flag", func(t *testing.T) {
		t.Parallel()
		ar := &AllowedRequests{
			Responses:       true,
			ResponsesDelete: true,
		}
		if ar.IsOperationAllowed(ResponsesRetrieveRequest) {
			t.Fatal("retrieve should be denied when only delete is enabled")
		}
		if !ar.IsOperationAllowed(ResponsesDeleteRequest) {
			t.Fatal("delete should be allowed")
		}
	})

	t.Run("explicit retrieve without other lifecycle flags", func(t *testing.T) {
		t.Parallel()
		ar := &AllowedRequests{
			Responses:         false,
			ResponsesRetrieve: true,
		}
		if !ar.IsOperationAllowed(ResponsesRetrieveRequest) {
			t.Fatal("retrieve should be allowed")
		}
		if ar.IsOperationAllowed(ResponsesDeleteRequest) {
			t.Fatal("delete should be denied")
		}
	})
}
