# PET 照片保留与限流（国内 staging v1）

- 对象：自 `device_pet_photos.created_at` 起 24 小时；解绑/撤权则尽早清理。
  Gateway 按 worker claim 验证对象键后，先删私有 OSS 对象：`cleared` 推进 `expired`；
  `finalized` 即使设备 ACK 离线仍转 `expiring`，下次发送 `cancel` 清设备单槽后进入 `dead`。
  删除失败仅重试，不把用户可见记录标为已清理。`GetPetPhoto` 同时强制当前 binding 与过期门禁。
- 墓碑：`expired/dead` 进入终态后再保留 7 天，由同一 worker 在空闲时以每批 100 行
  清理，既有 `device_events`/审计记录保留但图片过期不可读。批处理失败不清理其他状态。
- 入站 ready：Gateway 对同一设备串行化处理，同一 trigger ID + 原绑定 + hash + 字节数 +
  拍摄时间幂等；非重复照片每 30 秒最多接受 1 张，每小时最多 120 张（服务器收到时间计算）。
  超限 ready 不生成用户可见事件，但保留一个 `expiring` 事务让 worker 下发 `cancel` 清设备单槽，
  然后记为 `dead`。设备 MQTT `PET` 推理频率不因 Gateway 限流改变。
- 拍摄时间：新固件 `pet_photo_ready_v1` 的 `captured_at_unix_ms` 和最终事件中的同字段
  必须与首次抓拍时间一致；Gateway 比对事务记录并作为业务事件 `occurred_at`。
  老固件缺字段时保持兼容，仍以提交时的 `occurred_at` 表示事件时间。
- 没有管理员“删除所有历史照片”的新接口；业务保留期/更长期合规保留须另行审批。
