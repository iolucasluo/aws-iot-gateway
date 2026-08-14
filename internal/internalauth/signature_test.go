package internalauth

import (
	"testing"
)

func TestSignMatchesLivPetHMACV2KnownAnswer(t *testing.T) {
	t.Parallel()

	signature, err := Sign("test-secret-hmac-v2", SignatureRequest{
		APIKey:    "ak_test_hmac_v2",
		Timestamp: "00000011718000000000",
		Nonce:     "550e8400-e29b-41d4-a716-446655440000",
		Method:    "post",
		Path:      "/api/v1/openapi/devices/EXFDR%2f001/commands",
		RawQuery:  "z=last&tag=b&space=hello+world&tag=a&empty=&encoded=%7e",
		Body:      []byte(`{"wait_ack":true,"commands":[{"code":"feed","value":{"portions":1}}]}`),
	})
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}
	const want = "34501739cc0e22570570539157ab73cdd28c67ec2849b654035bb493ed963957"
	if signature != want {
		t.Fatalf("Sign() = %q, want %q", signature, want)
	}
}

func TestCanonicalizeMatchesLivPetHMACV2(t *testing.T) {
	t.Parallel()

	path, err := CanonicalizePath("/devices/EXFDR%2f001/~status")
	if err != nil {
		t.Fatalf("CanonicalizePath() error = %v", err)
	}
	if path != "/devices/EXFDR%2F001/~status" {
		t.Fatalf("CanonicalizePath() = %q", path)
	}
	query, err := CanonicalizeQuery("z=last&tag=b&space=hello+world&tag=a&empty=&encoded=%7e")
	if err != nil {
		t.Fatalf("CanonicalizeQuery() error = %v", err)
	}
	if query != "empty=&encoded=~&space=hello%20world&tag=a&tag=b&z=last" {
		t.Fatalf("CanonicalizeQuery() = %q", query)
	}
	if _, err := CanonicalizePath("/devices/%zz"); err == nil {
		t.Fatal("CanonicalizePath() should reject malformed escapes")
	}
	if _, err := CanonicalizeQuery("device=%zz"); err == nil {
		t.Fatal("CanonicalizeQuery() should reject malformed escapes")
	}
}

func TestSignRejectsNonCanonicalHeaders(t *testing.T) {
	t.Parallel()

	base := SignatureRequest{
		APIKey:    "ak_test_hmac_v2",
		Timestamp: "00000011718000000000",
		Nonce:     "550e8400-e29b-41d4-a716-446655440000",
		Method:    "GET",
		Path:      "/api/v1/openapi/devices",
	}
	for name, mutate := range map[string]func(*SignatureRequest){
		"short timestamp": func(request *SignatureRequest) { request.Timestamp = "1718000000000" },
		"unsafe nonce":    func(request *SignatureRequest) { request.Nonce = "contains spaces here" },
		"header newline":  func(request *SignatureRequest) { request.APIKey = "key\nforged" },
	} {
		t.Run(name, func(t *testing.T) {
			request := base
			mutate(&request)
			if _, err := Sign("secret", request); err == nil {
				t.Fatal("Sign() error = nil, want invalid canonical request")
			}
		})
	}
}
