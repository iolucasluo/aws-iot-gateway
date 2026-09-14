// Package devicesn 实现 Explorex device_sn 编号规范（EXF1-XXXXXXXX-C / EXC1-XXXXXXXX-C）
// 在 Gateway 侧的校验与归一化。
//
// 算法与测试向量一次性移植自平台仓库的
// explorex-cloud/services/explorex-api/internal/platform/devicesn（develop-lzc@ac51f073）。
// 本包是**自持实现**：不 import 平台仓库，也不构成构建或运行期依赖；两侧一致性由
// devicesn_test.go 中由平台生成器产出的冻结向量锁定。
//
// 只移植激活路径需要的能力。发号仍由平台产线 claim 承担；Gateway 仅在缺少外部来源时
// 使用自身的 randomDeviceSN() 兜底，避免形成第二套发号实现。
package devicesn

import (
	"errors"
	"strings"
)

// ErrInvalidDeviceSN 表示输入不满足 device_sn 编号规范。
var ErrInvalidDeviceSN = errors.New("devicesn: invalid device sn")

const (
	totalLength     = 15
	prefixLength    = 4
	firstDashIndex  = 4
	secondDashIndex = 13
)

// alphabet37 是 ISO 7064 MOD 37,2 的标准字符表 A_37。
const alphabet37 = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ*"

// AllowedPrefixes 返回受支持的产品族前缀白名单（与平台 AllowedPrefixes 一致）。
func AllowedPrefixes() []string {
	return []string{"EXF1", "EXC1"}
}

func isAllowedPrefix(prefix string) bool {
	for _, candidate := range AllowedPrefixes() {
		if candidate == prefix {
			return true
		}
	}
	return false
}

// isCrockfordChar 判断单字符是否属于 Crockford Base32（去掉易混的 I L O U）。
func isCrockfordChar(value byte) bool {
	switch {
	case value >= '0' && value <= '9':
		return true
	case value >= 'A' && value <= 'Z':
		return value != 'I' && value != 'L' && value != 'O' && value != 'U'
	default:
		return false
	}
}

func isCrockfordSegment(value string) bool {
	for index := 0; index < len(value); index++ {
		if !isCrockfordChar(value[index]) {
			return false
		}
	}
	return true
}

func isUpperOnly(value string) bool {
	for index := 0; index < len(value); index++ {
		if value[index] >= 'a' && value[index] <= 'z' {
			return false
		}
	}
	return true
}

func indexInAlphabet37(value byte) int {
	switch {
	case value >= '0' && value <= '9':
		return int(value - '0')
	case value >= 'A' && value <= 'Z':
		return int(value-'A') + 10
	case value == '*':
		return 36
	default:
		return -1
	}
}

// computeChecksum 计算 ISO 7064 MOD 37,2 校验位（Pure System Recursive Method）。
// 输入含非法字符时返回 0，调用方在此之前已完成字符集校验。
func computeChecksum(data string) byte {
	product := 0
	for index := 0; index < len(data); index++ {
		value := indexInAlphabet37(data[index])
		if value < 0 {
			return 0
		}
		product = ((product + value) * 2) % 37
	}
	return alphabet37[(38-product)%37]
}

// IsValid 报告 value 是否为合法且已归一化（全大写、无空白）的 device_sn。
func IsValid(value string) bool {
	if len(value) != totalLength || value[firstDashIndex] != '-' || value[secondDashIndex] != '-' {
		return false
	}
	prefix := value[:prefixLength]
	random := value[firstDashIndex+1 : secondDashIndex]
	check := value[secondDashIndex+1:]
	if !isUpperOnly(prefix) || !isUpperOnly(random) || !isUpperOnly(check) {
		return false
	}
	if !isAllowedPrefix(prefix) {
		return false
	}
	if !isCrockfordSegment(random) || !isCrockfordSegment(check) {
		return false
	}
	return computeChecksum(prefix+random) == check[0]
}

// Normalize 去首尾空白并转大写；归一化后仍不合法时返回原值与 ErrInvalidDeviceSN。
func Normalize(value string) (string, error) {
	normalized := strings.ToUpper(strings.TrimSpace(value))
	if !IsValid(normalized) {
		return value, ErrInvalidDeviceSN
	}
	return normalized, nil
}
