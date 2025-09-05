package template

var DummyNetPlan = `network:
  version: 2
  renderer: networkd
  dummy-devices:
    %s:
      addresses:
        - %s
`
