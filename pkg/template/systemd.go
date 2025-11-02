package template

var SystemdTemplate = `[Unit]
Description=Elchi Envoy (%s)
Requires=network-online.target
After=network-online.target

[Service]
Type=simple

WorkingDirectory=/var/lib/elchi

User=envoyuser
Group=envoyuser
AmbientCapabilities=CAP_NET_BIND_SERVICE CAP_NET_ADMIN CAP_NET_RAW CAP_DAC_OVERRIDE CAP_FOWNER CAP_SYS_RESOURCE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE CAP_NET_ADMIN CAP_NET_RAW CAP_DAC_OVERRIDE CAP_FOWNER CAP_SYS_RESOURCE
NoNewPrivileges=yes

ReadWritePaths=/var/log /var/lib/elchi /tmp /var/run /dev/shm
ReadOnlyPaths=/etc/ssl/certs

LimitNOFILE=1048576
LimitCORE=infinity
TasksMax=infinity

ExecStartPre=/var/lib/elchi/envoys/%s/envoy \
  -c /var/lib/elchi/bootstraps/%s.yaml --mode validate

ExecStart=/usr/bin/env python3 /var/lib/elchi/hotrestarter/hotrestarter.py \
  "/var/lib/elchi/envoys/%s/envoy \
     -c /var/lib/elchi/bootstraps/%s.yaml \
     --base-id %d \
     --log-path /var/log/elchi/%s_system.log \
     --drain-time-s 10 \
     --parent-shutdown-time-s 20"

ExecReload=/bin/kill -HUP $MAINPID
ExecStop=/bin/kill -TERM $MAINPID
KillMode=process

Restart=on-failure
RestartSec=30

SyslogIdentifier=elchi-%s

[Install]
WantedBy=multi-user.target
`
