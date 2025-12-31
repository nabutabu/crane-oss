# Host states and transitions
## Host States
- Provisioning
- Ready
- Draining
- Terminated
- Unhealthy

## Legal Transitions
- PROVISIONING -> READY
- READY -> DRAINING
- DRAINING -> TERMINATED
- READY -> UNHEALTHY
- UNHEALTHY -> DRAINING