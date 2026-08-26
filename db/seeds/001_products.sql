-- Dev/staging activation product catalog. Apply explicitly after migrations.
-- Production product initialization must use separately reviewed production values.

INSERT INTO products (id, pid, name, status) VALUES
    ('10000000-0000-0000-0000-000000000001', 'feeder-x1', 'Feeder X1', 'active'),
    ('10000000-0000-0000-0000-000000000002', 'minion-cam-x1', 'Minion Cam X1', 'active'),
    ('10000000-0000-0000-0000-000000000003', 'lamp-x1', 'Lamp X1', 'active')
ON CONFLICT (pid) DO NOTHING;
