package template

const JournalConf = `[Journal]
Storage=persistent
SystemMaxUse=1G
SystemMaxFileSize=100M
SystemMaxFiles=10
MaxRetentionSec=30day
Compress=yes
RateLimitIntervalSec=10s
RateLimitBurst=50000
`