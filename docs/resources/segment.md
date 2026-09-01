# openwrt_segment

Orchestrates a complete routed segment across:

- `/etc/config/network` interface
- `/etc/config/dhcp` pool
- `/etc/config/firewall` zone
- optional zone forwarding to `wan`

Each apply is done through managed blocks with commit + service restart.

