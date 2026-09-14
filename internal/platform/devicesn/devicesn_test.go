package devicesn

import (
	"errors"
	"testing"
)

// frozenValid / frozenInvalid 是跨服务冻结向量：由平台实现
// explorex-cloud/services/explorex-api/internal/platform/devicesn
// （develop-lzc@ac51f073）的 cmd/gen_vectors 于 2026-09-14 生成。
// 它们锁定 Gateway 侧移植实现与平台编号规范一致，防止双实现漂移。
var frozenValid = []string{
	"EXF1-PACSNBZY-5",
	"EXF1-C3SHD1AE-F",
	"EXF1-H0G782K7-V",
	"EXF1-2RCMPF4B-J",
	"EXF1-91AEGPTD-C",
	"EXF1-H97H97JY-S",
	"EXF1-01WRGVN9-W",
	"EXC1-VSGBKR96-C",
	"EXC1-7V5Q9C5S-B",
	"EXC1-9S8NQ2H2-6",
}

var frozenInvalid = []string{
	"",
	"EXF1",
	"EXF1-XXXXXXXX",
	"EXF1-XXXXXXXX-CC",
	"EXF1XXXXXXXXC--",
	"EXF1_XXXXXXXX_C",
	"ABCD-XXXXXXXX-X",
	"EXF9-XXXXXXXX-X",
	"EXC9-XXXXXXXX-X",
	"exf1-xxxxxxxx-c",
	"EXF1-IIIIIIII-1",
	"EXF1-LLLLLLLL-1",
	"EXF1-OOOOOOOO-1",
	"EXF1-UUUUUUUU-1",
	"EXF1-XXXXXXXX-I",
	"EXF1-XXXXXXXX-*",
	"EXF1-XXXX#XXX-1",
	"EXF1-XXXX XXX-1",
	"EXF1-12345678-0",
	"EXC1-ABCDEFGH-0",
	"EXF1-1234567Z-A",
}

func TestIsValidAcceptsFrozenValidVectors(t *testing.T) {
	for _, sn := range frozenValid {
		if !IsValid(sn) {
			t.Fatalf("IsValid(%q) = false, want true", sn)
		}
	}
}

func TestIsValidRejectsFrozenInvalidVectors(t *testing.T) {
	for _, sn := range frozenInvalid {
		if IsValid(sn) {
			t.Fatalf("IsValid(%q) = true, want false", sn)
		}
	}
}

// ISO 7064 MOD 37,2 对单字符替换与相邻交换保证 100% 检出。
func TestIsValidDetectsSubstitutionAndTransposition(t *testing.T) {
	for _, candidate := range []string{
		"EXF1-APCSNBZY-5", // 相邻交换
		"EXF1-QACSNBZY-5", // 单字符替换
	} {
		if IsValid(candidate) {
			t.Fatalf("IsValid(%q) = true, want false", candidate)
		}
	}
}

func TestNormalizeTrimsAndUppercases(t *testing.T) {
	normalized, err := Normalize("  exf1-pacsnbzy-5  ")
	if err != nil || normalized != "EXF1-PACSNBZY-5" {
		t.Fatalf("Normalize() = (%q, %v), want (EXF1-PACSNBZY-5, nil)", normalized, err)
	}
}

func TestNormalizeRejectsInvalidAndReturnsOriginal(t *testing.T) {
	const invalid = "EXF1-12345678-0"
	normalized, err := Normalize(invalid)
	if !errors.Is(err, ErrInvalidDeviceSN) || normalized != invalid {
		t.Fatalf("Normalize() = (%q, %v), want (%q, ErrInvalidDeviceSN)", normalized, err, invalid)
	}
}

func TestAllowedPrefixesIsFrozen(t *testing.T) {
	prefixes := AllowedPrefixes()
	if len(prefixes) != 2 || prefixes[0] != "EXF1" || prefixes[1] != "EXC1" {
		t.Fatalf("AllowedPrefixes() = %v, want [EXF1 EXC1]", prefixes)
	}
}
