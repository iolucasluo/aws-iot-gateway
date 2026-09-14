package postgres

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"strings"
	"testing"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/activation"
)

// identityConfirmingProvisioner 让集成测试跑完 confirm 闭环，同时不触碰 AWS IoT。
type identityConfirmingProvisioner struct{ activated []string }

func (p *identityConfirmingProvisioner) Provision(_ context.Context, deviceSN, _ string) (activation.PreparedIdentity, error) {
	return activation.PreparedIdentity{
		DeviceID:       deviceSN,
		CertificateID:  "certificate-" + deviceSN,
		CertificatePEM: "-----BEGIN CERTIFICATE-----\nfixture\n-----END CERTIFICATE-----",
		Endpoint:       "endpoint-ats.iot.example",
	}, nil
}

func (p *identityConfirmingProvisioner) Activate(_ context.Context, certificateID string) error {
	p.activated = append(p.activated, certificateID)
	return nil
}

// newCSRWithKey 生成真实 ECDSA P-256 私钥与 CSR：设备安装证明必须由该私钥签名，
// 因此不能像只测 prepare 的用例那样使用占位 PEM。
func newCSRWithKey(t *testing.T) (string, *ecdsa.PrivateKey) {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, privateKey)
	if err != nil {
		t.Fatalf("CreateCertificateRequest() error = %v", err)
	}
	csrPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER}))
	return csrPEM, privateKey
}

// installationProof 复刻设备端证明：对固定域串 + activation id + nonce 的 SHA-256 摘要签名。
// 该域串与 activation 包内的 installationProofMessage 必须一致。
func installationProof(t *testing.T, activationID, nonce string, key *ecdsa.PrivateKey) string {
	t.Helper()
	message := "ExploreX-Activation-Code-Commissioning-v1/install:" + activationID + ":" + nonce
	digest := sha256.Sum256([]byte(message))
	signature, err := ecdsa.SignASN1(rand.Reader, key, digest[:])
	if err != nil {
		t.Fatalf("SignASN1() error = %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(signature)
}

// 这是新固件的真实路径：设备 prepare 时上报本机 device_sn，但不上报 chip_uid
// （chip UID 需要 ak_efuse.ko，普通 boot 不加载，因此第一版只上报 SN）。
// 必须复用既有 device_registry 行与同一 device_sn，否则"每次重置都换号"复现。
func TestActivationReusesDeviceSNReportedByDevice(t *testing.T) {
	ctx := context.Background()
	pool := newIsolatedMigrationPool(t, ctx)
	if err := ApplyMigrations(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := ApplySeeds(ctx, pool); err != nil {
		t.Fatal(err)
	}
	repository, err := NewActivationRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	provisioner := &identityConfirmingProvisioner{}
	service := activation.NewServiceWithRepository(provisioner, repository)

	// 第一次激活：设备全新，没有可上报的 SN，Gateway 兜底铸号。
	firstCSR, firstKey := newCSRWithKey(t)
	first := createIdentityActivation(t, ctx, service, "feeder-x1", "device-sn-first", "")
	firstInstallation, err := service.Prepare(ctx, activation.PrepareInput{
		ActivationID: first.ID, Code: first.Code, CSRPEM: firstCSR,
		ExpectedMethod: activation.MethodBLE,
	})
	if err != nil {
		t.Fatalf("first Prepare() error = %v", err)
	}
	issuedSN := firstInstallation.DeviceID
	if !strings.HasPrefix(issuedSN, "fdr-") {
		t.Fatalf("first installation device sn = %q, want gateway-issued fdr- prefix", issuedSN)
	}
	if _, err := service.Confirm(ctx, activation.ConfirmInput{
		ActivationID: first.ID, Code: first.Code,
		Proof: installationProof(t, first.ID, firstInstallation.ProofNonce, firstKey),
	}); err != nil {
		t.Fatalf("first Confirm() error = %v", err)
	}

	// 第二次激活：设备已持有 SN（OTA/重置后重新配网），prepare 时上报该 SN。
	// 复用既有 registry 行的前提是旧归属已释放：uniq_device_bindings_active_device 保证
	// 一台设备只有一个 active binding，未释放时 Confirm 会返回 ErrDeviceBindingConflict。
	// 真实释放由 owner 的解绑/recommission 流程完成，这里按同样的终态预置。
	if _, err := pool.Exec(ctx, `
		UPDATE device_bindings SET status='inactive', released_at=now(), release_reason='recommission'
		WHERE status='active'
	`); err != nil {
		t.Fatalf("release previous binding: %v", err)
	}
	secondCSR, secondKey := newCSRWithKey(t)
	second := createIdentityActivation(t, ctx, service, "feeder-x1", "device-sn-second", "")
	reported, err := service.Prepare(ctx, activation.PrepareInput{
		ActivationID: second.ID, Code: second.Code, CSRPEM: secondCSR,
		ExpectedMethod: activation.MethodBLE, DeviceSN: issuedSN,
	})
	if err != nil {
		t.Fatalf("second Prepare() error = %v", err)
	}
	if reported.DeviceID != issuedSN {
		t.Fatalf("device sn changed across re-provisioning: %q -> %q", issuedSN, reported.DeviceID)
	}
	confirmed, err := service.Confirm(ctx, activation.ConfirmInput{
		ActivationID: second.ID, Code: second.Code,
		Proof: installationProof(t, second.ID, reported.ProofNonce, secondKey),
	})
	if err != nil {
		t.Fatalf("second Confirm() error = %v", err)
	}
	if confirmed.Status != activation.StatusActivated {
		t.Fatalf("activation status = %q, want activated", confirmed.Status)
	}
	if len(provisioner.activated) != 2 {
		t.Fatalf("activated certificates = %v, want one per activation", provisioner.activated)
	}

	// 第三次重置语义：再上报同一 SN，仍复用同一行；台账始终只有一行。
	if _, err := pool.Exec(ctx, `
		UPDATE device_bindings SET status='inactive', released_at=now(), release_reason='recommission'
		WHERE status='active'
	`); err != nil {
		t.Fatalf("release binding before third activation: %v", err)
	}
	thirdCSR, thirdKey := newCSRWithKey(t)
	third := createIdentityActivation(t, ctx, service, "feeder-x1", "device-sn-third", "")
	thirdInstallation, err := service.Prepare(ctx, activation.PrepareInput{
		ActivationID: third.ID, Code: third.Code, CSRPEM: thirdCSR,
		ExpectedMethod: activation.MethodBLE, DeviceSN: issuedSN,
	})
	if err != nil {
		t.Fatalf("third Prepare() error = %v", err)
	}
	if thirdInstallation.DeviceID != issuedSN {
		t.Fatalf("device sn changed on third activation: %q", thirdInstallation.DeviceID)
	}
	if _, err := service.Confirm(ctx, activation.ConfirmInput{
		ActivationID: third.ID, Code: third.Code,
		Proof: installationProof(t, third.ID, thirdInstallation.ProofNonce, thirdKey),
	}); err != nil {
		t.Fatalf("third Confirm() error = %v", err)
	}

	var totalRows int
	var rowSN string
	var chipUID *[]byte
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM device_registry
	`).Scan(&totalRows); err != nil {
		t.Fatal(err)
	}
	if totalRows != 1 {
		t.Fatalf("device_registry rows after three activations = %d, want 1", totalRows)
	}
	if err := pool.QueryRow(ctx, `
		SELECT device_sn, chip_uid FROM device_registry
	`).Scan(&rowSN, &chipUID); err != nil {
		t.Fatal(err)
	}
	if rowSN != issuedSN {
		t.Fatalf("registry device sn = %q, want %q", rowSN, issuedSN)
	}
	if chipUID != nil {
		t.Fatalf("chip_uid = %v, want NULL when the device reports only its SN", *chipUID)
	}
}
