ALTER TABLE nodes ADD COLUMN tunnel_address INET;

ALTER TABLE forward_rules ADD COLUMN relay_port INTEGER;
ALTER TABLE forward_rules ADD COLUMN egress_config_version BIGINT;
UPDATE forward_rules SET relay_port=listen_port WHERE mode='relay';
UPDATE forward_rules r SET egress_config_version=n.config_version
FROM nodes n WHERE r.mode='relay' AND n.id=r.egress_node_id;
ALTER TABLE forward_rules ADD CONSTRAINT forward_rules_relay_port_check CHECK (
    (mode='direct' AND relay_port IS NULL AND egress_config_version IS NULL) OR
    (mode='relay' AND relay_port BETWEEN 1 AND 65535 AND egress_config_version>0)
);

CREATE UNIQUE INDEX forward_rules_relay_listener_idx
    ON forward_rules(egress_node_id,relay_port) WHERE mode='relay';
