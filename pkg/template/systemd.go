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
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
NoNewPrivileges=yes

LimitNOFILE=1048576
LimitCORE=infinity
TasksMax=infinity

ExecStartPre=/var/lib/elchi/envoys/%s/envoy \
  -c /var/lib/elchi/bootstraps/%s.yaml --mode validate

ExecStart=/usr/bin/env python3 /var/lib/elchi/hotrestarter/hotrestarter.py \
  "/var/lib/elchi/envoys/%s/envoy \
     -c /var/lib/elchi/bootstraps/%s.yaml \
     --base-id %d \
     --drain-time-s 10 \
     --parent-shutdown-time-s 20"

ExecReload=/bin/kill -HUP $MAINPID
ExecStop=/bin/kill -TERM $MAINPID
KillMode=process

Restart=on-failure
RestartSec=30

LogNamespace=elchi-%s
StandardOutput=journal
StandardError=journal
SyslogIdentifier=elchi-%s

[Install]
WantedBy=multi-user.target
`
