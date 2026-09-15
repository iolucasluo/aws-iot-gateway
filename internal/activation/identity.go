package activation

import (
	"encoding/hex"
	"regexp"
	"strings"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/platform/devicesn"
)

const (
	minChipUIDHexLength = 4
	maxChipUIDHexLength = 64
)

// legacyDeviceSNPattern 兼容既有设备：产线标准落地前签发的 device_sn 不是 EXF1-/EXC1-
// 格式（存量行使用 fdr-<32hex>）。标准形状走编号规范校验，其余只做保守字符集校验，
// 否则重激活老设备会被拒绝。
var legacyDeviceSNPattern = regexp.MustCompile("^[A-Za-z0-9._-]{1,64}$")

// Reservation 描述一次 activation 的设备身份预留。
type Reservation struct {
	ActivationID     string
	DeviceInternalID string
	DeviceSN         string
	// DeviceSNGenerated 标识 DeviceSN 是 caller/device 未申报时的兜底铸号。
	// 仅用于归并冲突判定，不持久化：设备身份始终只由 device_sn 表达。
	DeviceSNGenerated bool
	ChipUID           []byte
	CSRPEM            string
}

// normalizeReportedDeviceSN 归一化外部上报的 device_sn。
func normalizeReportedDeviceSN(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", nil
	}
	upper := strings.ToUpper(trimmed)
	if len(upper) == 15 && upper[4] == '-' && upper[13] == '-' {
		if !devicesn.IsValid(upper) {
			return "", ErrInvalidDeviceSN
		}
		return upper, nil
	}
	if !legacyDeviceSNPattern.MatchString(trimmed) {
		return "", ErrInvalidDeviceSN
	}
	return trimmed, nil
}

// normalizeChipUID 把 hex 形式的 chip UID 解析为字节；空值返回 nil。
func normalizeChipUID(value string) ([]byte, error) {
	trimmed := strings.ToLower(strings.TrimSpace(value))
	if trimmed == "" {
		return nil, nil
	}
	if len(trimmed) < minChipUIDHexLength || len(trimmed) > maxChipUIDHexLength || len(trimmed)%2 != 0 {
		return nil, ErrInvalidChipUID
	}
	decoded, err := hex.DecodeString(trimmed)
	if err != nil || len(decoded) == 0 {
		return nil, ErrInvalidChipUID
	}
	return decoded, nil
}

// resolveDeviceIdentity 合并调用方与设备上报的身份事实；设备身份只用 device_sn 表达。
// 返回的 device_sn 为空表示需要兜底生成。
func resolveDeviceIdentity(callerSN string, callerChipUID []byte, deviceSN string, deviceChipUID []byte) (string, []byte) {
	chipUID := callerChipUID
	if len(chipUID) == 0 {
		chipUID = deviceChipUID
	}
	if callerSN != "" {
		return callerSN, chipUID
	}
	if deviceSN != "" {
		return deviceSN, chipUID
	}
	return "", chipUID
}
