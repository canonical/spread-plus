# OpenStack Backend

The OpenStack backend integrates Spread with an OpenStack cloud. Spread uses the Nova compute
API to boot VMs on demand, runs the test suite over SSH, and then deletes the instances when
finished.

All OpenStack API calls are made through the
[goose](https://github.com/go-goose/goose) library (`github.com/go-goose/goose/v5`), which
provides Go clients for the Nova (compute), Neutron (networking), and Glance (image) services.

## How it works

### Instance lifecycle

```
Spread                              OpenStack APIs
  |                                       |
  |-- Nova RunServer ------------------->|  create instance, get server ID
  |-- GetServer (poll) ----------------->|  wait for status to leave BUILD
  |-- GetServer (poll) ----------------->|  wait for IP address assignment
  |                                       |
  |  [watch serial console for MACHINE-IS-READY]
  |                                       |
  |  [SSH connect + send project files]  |
  |  [run test suite over SSH]            |
  |                                       |
  |-- Nova DeleteServer ---------------->|  delete instance
  |-- GetServer (poll) ----------------->|  confirm deletion
```

### Allocation phase

`Allocate` calls `createMachine`, which performs several sequential steps.

**Resolve resources:**
- **Flavor**: matched by exact name against the available flavors. Defaults to `m1.medium`; can
  be overridden by the backend-level `plan` or a per-system `plan`.
- **Networks**: if no networks are specified for the system, the first non-external network found
  is used. Named networks are looked up by exact name.
- **Security groups**: looked up by exact name from the list in the system's `groups` field.
- **Image**: resolved in three passes, stopping at the first match:
  1. Exact name match.
  2. Prefix match — the image name starts with the given value; if multiple match, the newest is used.
  3. Term match — every whitespace/separator-separated term in the given value appears in the
     image name; if multiple match, the newest is used.
  Only images with status `active` are considered.

**Boot the instance:**
- The Spread password is hashed with `openssl passwd -6` and embedded in a cloud-init
  `#cloud-config` script that also enables `PermitRootLogin` and `PasswordAuthentication` in
  sshd, then writes the `MACHINE-IS-READY` marker to the serial console on completion.
- The instance is booted from a volume (block device mapping) rather than an ephemeral disk.
  Volume size is resolved as: explicit `storage` → image's `MinimumDisk` → 20 GB default.
  Volumes are deleted on instance termination by default (`volume-auto-delete: true`).
- Instance metadata tags set at creation: `spread=true`, `owner`, `reuse`, `project` (when set),
  and `halt-timeout=<duration>` (when set). These tags are used later by garbage collection.
- If `location` is set on the backend, the instance is placed in that availability zone.

**Wait for provisioning** (`waitProvision`):
- Polls `GetServer` every 5 seconds until the instance status leaves `BUILD`.
- Timeout is `wait-timeout` when configured, otherwise 3 minutes.
- If the instance reaches `ERROR` state and a `Fault` is present, the error message and details
  are optionally saved to `<name>.result.log` (when `--logs` is set) before returning the error.
- Any status other than `ACTIVE` causes the allocation to fail.

**Obtain the IP address:**
- Polls `GetServer` every 5 seconds until an address is present (30-second hard timeout).
- When the system specifies networks, the address from the first listed network is used.
  Otherwise, the first IP found across all networks is used.

**Wait for the OS to boot** (`waitServerBoot`):
- Reads the serial console output every 5 seconds and looks for the `MACHINE-IS-READY` string
  written by cloud-init (5-minute hard timeout; a warning is logged every 60 seconds).
- If the serial console is unavailable (e.g. the cloud does not expose it), this step is skipped
  and Spread proceeds directly to the SSH connect phase.

**Proxy address resolution** (when `proxy` is set):
- After the IP is obtained, Spread probes port 22 on the address directly (3-second timeout).
- If the direct probe fails and `proxy` is set, the address is remapped to `proxy:port` using
  the `cidr-port-rel` list. Each entry is a `CIDR:initial-port` pair; the port is computed as
  `initial-port + offset`, where offset is the number of IPs between the CIDR base and the
  allocated address.
- At least one `cidr-port-rel` entry is required when `proxy` is set.

If any step fails, Spread attempts to delete the instance before returning the error. If
deletion also fails, the error is wrapped in a `FatalError` to abort the entire run.

### Execution phase

Spread connects over SSH using the credentials in `username`/`password` (or via the proxy
address when applicable), uploads the project files, and runs suites and tasks. The OpenStack
instance remains running throughout; no Spread-side heartbeat is sent to the API.

### Discard phase

`Discard` calls `removeMachine`, which:
1. Checks whether the instance still exists (`GetServer`); skips deletion if already gone.
2. Calls `DeleteServer`.
3. Polls `GetServer` every 5 seconds until the status is `DELETED` or the call returns an error
   (3-minute timeout; logs a warning on timeout but does not fail).

Discard is triggered in the same situations as for other backends: normal teardown (unless
`-reuse`), SSH connect failure, project send failure, and explicit `-discard` invocation.

### Garbage collection

`GarbageCollect` lists all instances tagged `spread=true` and compares each instance's
`created_at` timestamp against the effective `halt-timeout`. The per-instance `halt-timeout`
metadata label takes precedence over the backend-level setting. Instances that have been running
longer than their timeout are deleted via `removeMachine`.

---

## Configuration reference

```yaml
backends:
    openstack:
        # Path to a dotenv file with OpenStack credentials.
        # May use $(HOST: …) to resolve at runtime.
        key: '$(HOST: echo "$SPREAD_OPENSTACK_ENV")'

        # Default flavor (compute plan) for all systems.  Per-system plan overrides this.
        plan: m1.medium

        # Instances running longer than this are cancelled by garbage collection.
        halt-timeout: 2h

        # OpenStack availability zone to place instances in.
        location: AZ1

        # Proxy hostname used when direct SSH to the instance IP is not reachable.
        proxy: proxy.example.com

        # List of CIDR:initial-port mappings used to compute the proxy port.
        # Required when proxy is set.
        cidr-port-rel:
            - 10.0.0.0/24:10000

        # Whether to delete the root volume when the instance is terminated.
        # Defaults to true.
        volume-auto-delete: true

        systems:
            - <system-name>:
                  # Glance image name (exact, prefix, or term match; newest wins).
                  image: ubuntu-focal-daily-amd64

                  # Override the backend-level flavor for this system.
                  plan: m1.large

                  # Networks to attach.  First listed network is used for SSH.
                  networks:
                      - network_external
                      - network_pvn

                  # Security groups to assign.
                  groups:
                      - group_external

                  # Root volume size in bytes (e.g. 20GB = 20*1024*1024*1024).
                  # Defaults to the image's minimum disk size, or 20 GB.
                  storage: 20G

                  # How long to wait for the instance to reach ACTIVE.
                  # Defaults to 3 minutes.
                  wait-timeout: 5m

                  # Parallel workers for this system.
                  workers: 5

                  username: root
                  password: '$(HOST: echo "$SPREAD_PASSWORD")'
```

---

## Authentication

Credentials are read from environment variables (or a dotenv file pointed to by `key`). At least
`OS_AUTH_URL` and one credential pair must be set. The authentication method is selected
automatically:

| Variables set | Method used |
|---|---|
| `OS_ACCESS_KEY` + `OS_SECRET_KEY` | Key-pair (EC2-style) |
| `OS_USERNAME` + `OS_PASSWORD` | Keystone UserPass v3 (or v2 when `OS_AUTH_VERSION`/`OS_IDENTITY_API_VERSION` is not 3) |

### Full list of supported environment variables

| Variable | Purpose |
|---|---|
| `OS_AUTH_URL` | Keystone endpoint |
| `OS_USERNAME` / `OS_ACCESS_KEY` | Username or access key |
| `OS_PASSWORD` / `OS_SECRET_KEY` | Password or secret key |
| `OS_REGION_NAME` | Region |
| `OS_TENANT_ID` / `OS_PROJECT_ID` | Project ID |
| `OS_TENANT_NAME` / `OS_PROJECT_NAME` | Project name |
| `OS_AUTH_VERSION` / `OS_IDENTITY_API_VERSION` | Keystone API version (2 or 3) |
| `OS_DEFAULT_DOMAIN_NAME` | Default domain (Keystone v3) |
| `OS_PROJECT_DOMAIN_NAME` | Project domain (Keystone v3) |
| `OS_USER_DOMAIN_NAME` | User domain (Keystone v3) |

The canonical way to populate these is to source your OpenStack RC file:

```sh
source ~/openstack-rc.sh
```

For more information see the
[OpenStack RC file documentation](https://docs.openstack.org/ocata/user-guide/common/cli-set-environment-variables-using-openstack-rc.html).

---

## Example

```yaml
backends:
    openstack:
        key: '$(HOST: echo "$SPREAD_OPENSTACK_ENV")'
        plan: cpu2-ram4-disk10
        halt-timeout: 2h
        systems:
            # Basic system using image prefix matching.
            - ubuntu-20.04:
                  image: ubuntu-focal-daily-amd64
                  workers: 5

            # System with explicit networks and security groups.
            - ubuntu-22.04:
                  image: ubuntu-jammy-daily-amd64
                  networks:
                      - network_external
                      - network_pvn
                  groups:
                      - group_external
                  workers: 3

            # System accessed through a proxy.
            # (backend-level proxy and cidr-port-rel must also be set)
            - ubuntu-internal:
                  image: ubuntu-noble-daily-amd64
                  plan: m1.large
                  storage: 20G
```
