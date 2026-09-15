package internalauth

// Permissions 是内部 App 与项目凭证可用的权限白名单。
//
// 它同时约束 INTERNAL_APPS_JSON（部署期静态来源）与 api_credentials（平台面 DB 来源），
// 并与 004 迁移的 api_credentials_permissions_check 保持一致：
// 任何新增权限都必须同时改这里和迁移，避免权限字符串自由漂移。
var Permissions = []string{
	"devices:read",
	"devices:operate",
	"ota:publish",
	"ota:deploy",
}

// ValidPermission 判断权限是否在白名单内。
func ValidPermission(permission string) bool {
	for _, candidate := range Permissions {
		if candidate == permission {
			return true
		}
	}
	return false
}
