-- Dev/staging activation product catalog. Apply explicitly after migrations.
-- Production product initialization must use separately reviewed production values.

INSERT INTO products (id, pid, name, status, capabilities) VALUES
    ('10000000-0000-0000-0000-000000000001', 'feeder-x1', 'Feeder X1', 'active',
        '["webrtc.v1", "local-media.v1", "ptz.v1"]'::jsonb),
    ('10000000-0000-0000-0000-000000000002', 'minion-cam-x1', 'Minion Cam X1', 'active',
        '["webrtc.v1"]'::jsonb),
    ('10000000-0000-0000-0000-000000000003', 'lamp-x1', 'Lamp X1', 'active',
        '[]'::jsonb)
ON CONFLICT (pid) DO UPDATE SET
    name = EXCLUDED.name,
    status = EXCLUDED.status,
    capabilities = EXCLUDED.capabilities;
