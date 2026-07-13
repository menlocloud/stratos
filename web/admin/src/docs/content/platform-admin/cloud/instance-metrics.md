# Instance Utilization Graphs

Stratos can show per-instance utilization charts — CPU, memory, and network — inside the client portal. The numbers come from OpenStack's telemetry stack: Ceilometer samples the hypervisors, Gnocchi stores the resulting time series, and Stratos queries Gnocchi to draw the graphs.

## What OpenStack needs first

- **Ceilometer** running and polling compute metrics.
- **Gnocchi** running as the metric backend and reachable from Stratos through the service catalog.

On a kolla-ansible 2026.1 deployment, enable both:

```yaml
# /etc/kolla/globals.yml
enable_ceilometer: "yes"
enable_gnocchi: "yes"
```

Confirm samples are actually arriving before you switch the feature on in Stratos:

```bash
openstack metric resource list --type instance
openstack metric list
```

## Switching it on in Stratos

Go to **System > Cloud providers**, open the provider, enable the metrics toggle in the **Features** section, and save.

![Instance metrics feature toggle](/docs-img/instance-metrics-feature-toggle.png)

## The client's view

With the feature on, every server's detail page in the client portal grows a set of utilization charts — CPU usage, memory usage, and network throughput — drawn from that instance's Gnocchi time series.

<!-- screenshot: /docs-img/instance-metrics-client-charts.png — client portal: server detail page showing the CPU/memory/network charts rendered from Gnocchi data -->

## When the charts are empty

| Symptom | Likely cause |
|---|---|
| No data on any instance | Ceilometer isn't polling, or the Gnocchi endpoint is missing from the Keystone catalog. |
| No data on new instances only | The Gnocchi resource hasn't been created yet, or the Ceilometer polling interval hasn't come around. |
| Memory graph absent | The hypervisor driver doesn't report memory stats; CPU and network still populate. |
