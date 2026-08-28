-- Gateway-owned development/staging DP contract catalog.
-- The catalog is intentionally seed/migration managed; there is no runtime
-- Product/DP CRUD API in this service yet.

INSERT INTO product_dps (id, product_id, code, type, direction, spec, status) VALUES
    ('20000000-0000-0000-0000-000000000001', (SELECT id FROM products WHERE pid = 'feeder-x1'), 'feed', 'object', 'downlink_request_ack',
        '{"required":["portions"],"properties":{"portions":{"type":"integer"}},"allow_extra":false}'::jsonb, 'active'),
    ('20000000-0000-0000-0000-000000000002', (SELECT id FROM products WHERE pid = 'feeder-x1'), 'set_schedule', 'object', 'downlink_request_ack',
        '{"properties":{"schedules":{"type":"array"}}}'::jsonb, 'active'),
    ('20000000-0000-0000-0000-000000000101', (SELECT id FROM products WHERE pid = 'minion-cam-x1'), 'ptz', 'object', 'downlink_request_ack', '{"summary":"op/direction/steps or normalized position"}'::jsonb, 'active'),
    ('20000000-0000-0000-0000-000000000102', (SELECT id FROM products WHERE pid = 'minion-cam-x1'), 'snapshot', 'object', 'downlink_request_ack', '{"summary":"firmware snapshot action"}'::jsonb, 'active'),
    ('20000000-0000-0000-0000-000000000103', (SELECT id FROM products WHERE pid = 'minion-cam-x1'), 'video_res', 'object', 'downlink_request_ack', '{"summary":"resolution"}'::jsonb, 'active'),
    ('20000000-0000-0000-0000-000000000104', (SELECT id FROM products WHERE pid = 'minion-cam-x1'), 'night_mode', 'object', 'downlink_request_ack', '{"summary":"mode"}'::jsonb, 'active'),
    ('20000000-0000-0000-0000-000000000105', (SELECT id FROM products WHERE pid = 'minion-cam-x1'), 'motion_det', 'object', 'downlink_request_ack', '{"summary":"enabled/sensitivity/report interval"}'::jsonb, 'active'),
    ('20000000-0000-0000-0000-000000000106', (SELECT id FROM products WHERE pid = 'minion-cam-x1'), 'motion_track', 'object', 'downlink_request_ack', '{"summary":"enabled"}'::jsonb, 'active'),
    ('20000000-0000-0000-0000-000000000107', (SELECT id FROM products WHERE pid = 'minion-cam-x1'), 'record_start', 'object', 'downlink_request_ack', '{"summary":"start manual recording"}'::jsonb, 'active'),
    ('20000000-0000-0000-0000-000000000108', (SELECT id FROM products WHERE pid = 'minion-cam-x1'), 'record_stop', 'object', 'downlink_request_ack', '{"summary":"stop manual recording"}'::jsonb, 'active'),
    ('20000000-0000-0000-0000-000000000109', (SELECT id FROM products WHERE pid = 'minion-cam-x1'), 'sdcard_status', 'object', 'downlink_request_ack', '{"summary":"SD card status request/state ACK"}'::jsonb, 'active'),
    ('20000000-0000-0000-0000-000000000110', (SELECT id FROM products WHERE pid = 'minion-cam-x1'), 'reboot', 'object', 'downlink_request_ack', '{"summary":"optional delay_ms"}'::jsonb, 'active'),
    ('20000000-0000-0000-0000-000000000111', (SELECT id FROM products WHERE pid = 'minion-cam-x1'), 'device_info', 'object', 'downlink_request_ack', '{"summary":"device information request/state ACK"}'::jsonb, 'active'),
    ('20000000-0000-0000-0000-000000000112', (SELECT id FROM products WHERE pid = 'minion-cam-x1'), 'speaker_vol', 'object', 'downlink_request_ack', '{"summary":"volume"}'::jsonb, 'active'),
    ('20000000-0000-0000-0000-000000000113', (SELECT id FROM products WHERE pid = 'minion-cam-x1'), 'stream', 'object', 'downlink_request_ack', '{"summary":"typed media flow command"}'::jsonb, 'active'),
    ('20000000-0000-0000-0000-000000000114', (SELECT id FROM products WHERE pid = 'minion-cam-x1'), 'two_way_audio', 'object', 'downlink_request_ack', '{"summary":"enabled"}'::jsonb, 'active'),
    ('20000000-0000-0000-0000-000000000115', (SELECT id FROM products WHERE pid = 'minion-cam-x1'), 'audio_playback', 'object', 'downlink_request_ack', '{"summary":"audio playback"}'::jsonb, 'active'),
    ('20000000-0000-0000-0000-000000000116', (SELECT id FROM products WHERE pid = 'minion-cam-x1'), 'record_mode', 'object', 'downlink_request_ack', '{"summary":"record mode"}'::jsonb, 'active'),
    ('20000000-0000-0000-0000-000000000117', (SELECT id FROM products WHERE pid = 'minion-cam-x1'), 'record_clip', 'object', 'downlink_request_ack', '{"summary":"record clip"}'::jsonb, 'active'),
    ('20000000-0000-0000-0000-000000000118', (SELECT id FROM products WHERE pid = 'minion-cam-x1'), 'sdcard', 'object', 'downlink_request_ack', '{"summary":"SD card"}'::jsonb, 'active'),
    ('20000000-0000-0000-0000-000000000119', (SELECT id FROM products WHERE pid = 'minion-cam-x1'), 'sd_playback', 'object', 'downlink_request_ack', '{"summary":"SD playback"}'::jsonb, 'active'),
    ('20000000-0000-0000-0000-000000000120', (SELECT id FROM products WHERE pid = 'minion-cam-x1'), 'sd_files', 'object', 'downlink_request_ack', '{"summary":"SD file listing"}'::jsonb, 'active'),
    ('20000000-0000-0000-0000-000000000121', (SELECT id FROM products WHERE pid = 'minion-cam-x1'), 'recommission', 'object', 'downlink_request_ack', '{"required":["reset_scope"],"properties":{"reset_scope":{"type":"string"}},"allow_extra":false}'::jsonb, 'active'),
    ('20000000-0000-0000-0000-000000000122', (SELECT id FROM products WHERE pid = 'minion-cam-x1'), 'factory_reset', 'object', 'downlink_request_ack', '{"required":["scope"],"properties":{"scope":{"type":"string"}},"allow_extra":false}'::jsonb, 'active'),
    ('20000000-0000-0000-0000-000000000123', (SELECT id FROM products WHERE pid = 'minion-cam-x1'), 'reset_wifi', 'object', 'downlink_request_ack', '{"summary":"legacy reset Wi-Fi action"}'::jsonb, 'active'),
    ('20000000-0000-0000-0000-000000000124', (SELECT id FROM products WHERE pid = 'minion-cam-x1'), 'privacy_mode', 'object', 'downlink_request_ack', '{"summary":"enabled"}'::jsonb, 'active'),
    ('20000000-0000-0000-0000-000000000125', (SELECT id FROM products WHERE pid = 'minion-cam-x1'), 'alarm_siren', 'object', 'downlink_request_ack', '{"summary":"hardware capability pending confirmation"}'::jsonb, 'active'),
    ('20000000-0000-0000-0000-000000000126', (SELECT id FROM products WHERE pid = 'minion-cam-x1'), 'sound_det', 'object', 'downlink_request_ack', '{"summary":"enabled/sensitivity"}'::jsonb, 'active'),
    ('20000000-0000-0000-0000-000000000127', (SELECT id FROM products WHERE pid = 'minion-cam-x1'), 'alarm_config', 'object', 'downlink_request_ack', '{"summary":"zone and boundary policies"}'::jsonb, 'active'),
    ('20000000-0000-0000-0000-000000000128', (SELECT id FROM products WHERE pid = 'minion-cam-x1'), 'memory_info', 'object', 'downlink_request_ack', '{"summary":"memory information request/state ACK"}'::jsonb, 'active')
ON CONFLICT (product_id, code) DO UPDATE SET
    type = EXCLUDED.type,
    direction = EXCLUDED.direction,
    spec = EXCLUDED.spec,
    status = EXCLUDED.status;
