# Testflinger Backend

The Testflinger backend integrates Spread with [Testflinger](https://testflinger.readthedocs.io), a
service that queues and dispatches test jobs to physical or virtual devices. Spread submits a job
to a queue, waits for a device to be provisioned and reachable, runs the test suite over SSH, and
then cancels the job when finished.

## How it works

### Job lifecycle

```
Spread                          Testflinger server
  |                                     |
  |-- POST /job ----------------------->|  submit job, get job_id
  |                                     |
  |-- GET /result/{job_id} (poll) ----->|  wait for state "allocated" or "reserve"
  |   <-- device_ip --------------------|
  |                                     |
  |  [SSH connect + send project files] |
  |  [run test suite over SSH]          |
  |                                     |
  |-- POST /job/{job_id}/action cancel ->|  discard job
```

### Allocation phase

`Allocate` drives two sequential steps: **requesting** the device and **waiting** for it to boot.

**Requesting the device** (`requestDevice`):
- Builds the job payload with the target `queue`, optional `provision_data`, and the tags
  `spread` and `halt-timeout=<duration>`.
- Sets either `allocate_data.allocate=true` (default) or `reserve_data` (when `reserve-key`
  is configured).
- Submits the job via `POST /job` and records the returned `job_id`.

**Waiting for the device** (`waitDeviceBoot`):
- Polls `GET /result/{job_id}` every 15 seconds until the job state becomes `allocated` or
  `reserve`, or until `wait-timeout` is reached (default 15 minutes when not configured).
- Every 3 minutes a progress message is printed so long-running waits remain visible.
- In allocate mode the device IP is read from `device_info.device_ip` in the result.
- In reserve mode the IP may not appear in `device_info`, so Spread additionally scrapes
  `/result/{job_id}` and `/result/{job_id}/output` for a line matching
  `ssh … '<user>@<ip>'` and extracts the address via regex.
- If the job reaches state `cancelled`, `complete`, or `completed` before an address is
  available, the wait fails immediately.
- On any wait failure, Spread optionally saves the job's result output (see
  [Failure logging](#failure-logging)) and then discards the job before returning the error.

Once an address is obtained, Spread retries the full `Allocate` call for up to 5 minutes
(retrying every 5 seconds) if transient errors occur. After three consecutive complete
allocation cycles that all fail, the system is abandoned for this run.

After a successful allocation Spread:
1. Reserves the device IP to prevent another worker from reusing the same address.
2. Records the job in the reuse file (used by `-reuse` runs).
3. Opens an SSH connection and uploads the project files to the remote path.

### Execution phase

Spread runs suites and tasks over the established SSH connection. If sending project files
fails after allocation, the device is discarded immediately. During execution the device
remains allocated in Testflinger; the job stays in the `allocated` or `reserve` state until
Spread explicitly cancels it.

### Discard phase

`Discard` sends `POST /job/{job_id}/action` with `{"action": "cancel"}` to cancel the job on
the Testflinger server. Spread calls discard in the following situations:

| Trigger | When it happens |
|---------|-----------------|
| Normal teardown | After all tasks for a system finish (unless `-reuse` is active) |
| SSH connect failure | Immediately after allocation, if the SSH dial fails |
| Project send failure | If uploading project files to the device fails |
| Wait failure | If `waitDeviceBoot` times out or the job reaches a terminal state |
| `-discard` flag | When Spread is invoked with `-reuse-pid=<pid> -discard` to clean up a previous run |

After cancelling the job, Spread removes the entry from the reuse file and releases the
reserved IP address.

### Provisioning

The `image` field is a shorthand for the two most common cases:

| `image` value        | Sent to Testflinger as                     |
|---------------------|--------------------------------------------|
| URL (e.g. `https://…`) | `provision_data.url`                    |
| Any other string    | `provision_data.distro`                    |
| Same as system name or empty | No `provision_data` (no reprovisioning) |

For any other provisioning method (e.g. MAAS, OEM connectors), use `provision-data` instead.
Its contents are forwarded verbatim as the job's `provision_data`. Setting both `image` and
`provision-data` on the same system is an error.

### Garbage collection

`GarbageCollect` lists all jobs tagged `spread` that are still active
(`GET /job/search?tags=spread&state=active`). For each job it checks the elapsed time since
`created_at`. If that exceeds `halt-timeout` (or a per-job override set via a `halt-timeout=<duration>`
tag), the job is cancelled.

### Failure logging

When `--logs` is set and a device fails to allocate, Spread fetches the full job result
(`/result/{job_id}`) and writes `reserve_output`, `allocate_output`, `setup_output`, and
`provision_output` to `<logs-dir>/<job_id>.result.log`.

---

## Configuration reference

```yaml
backends:
    testflinger:
        # Path to a dotenv file containing TESTFLINGER_CLIENT_ID and
        # TESTFLINGER_SECRET_KEY.  May use $(HOST: …) to resolve at runtime.
        key: '$(HOST: echo "$SPREAD_TESTFLINGER_ENV")'

        # How long to wait for a device to become reachable after job submission.
        # Per-system wait-timeout overrides this value.
        wait-timeout: 30m

        # Jobs running longer than this are cancelled by garbage collection.
        # Also sent as the "halt-timeout=<duration>" tag on every job so that
        # external GC tools can enforce the same limit.
        halt-timeout: 2h

        systems:
            - <system-name>:
                  # Testflinger queue to submit to.  Defaults to the system name.
                  queue: <queue-name>

                  # Shorthand provisioning: URL → provision_data.url,
                  # string → provision_data.distro.
                  image: <url-or-distro>

                  # Full provisioning data passed verbatim to the device connector.
                  # Cannot be combined with "image".
                  provision-data:
                      distro: jammy
                      kernel: hwe-22.04
                      user_data: |
                          #cloud-config
                          packages:
                              - build-essential

                  # SSH credentials used by Spread to connect to the device.
                  username: ubuntu
                  password: <password>

                  # Enable reserve mode: import this key before handing over the device.
                  # Format: lp:<launchpad-id> or gh:<github-id>
                  reserve-key: lp:<KEY_NAME>

                  # RSA private key used to SSH into a reserved device.
                  ssh-rsa-key: '$(HOST: echo "$SPREAD_SSH_KEY")'
                  # Passphrase for the RSA key, if encrypted.
                  ssh-key-pass: '$(HOST: echo "$SPREAD_SSH_KEY_PASS")'

                  # Override the backend-level wait-timeout for this system only.
                  wait-timeout: 45m

                  # Maximum number of parallel jobs for this system.
                  workers: 2
```

---

## Authentication

If the Testflinger deployment requires authentication, set two environment variables before
running Spread:

```
TESTFLINGER_CLIENT_ID=<client-id>
TESTFLINGER_SECRET_KEY=<secret-key>
```

Alternatively, point the backend `key` setting at a dotenv file that contains those variables.
The legacy names `TF_CLIENT_ID` and `TF_SECRET_KEY` are also accepted as a fallback.

When both variables are present, Spread exchanges them for a bearer token via
`POST /v1/oauth2/token` and attaches it to every subsequent request.

---

## Environment variables

| Variable                  | Description                                              | Default                                  |
|---------------------------|----------------------------------------------------------|------------------------------------------|
| `TF_ENDPOINT`             | Base URL of the Testflinger server                       | `https://testflinger.canonical.com`      |
| `TF_API_VERSION`          | API version path segment                                 | `v1`                                     |
| `TESTFLINGER_CLIENT_ID`   | OAuth2 client ID for authenticated deployments           | —                                        |
| `TESTFLINGER_SECRET_KEY`  | OAuth2 secret key for authenticated deployments          | —                                        |

---

## Example

```yaml
backends:
    testflinger:
        wait-timeout: 30m
        halt-timeout: 2h
        systems:
            # Allocate mode with URL provisioning.
            - ubuntu-rpi3:
                  queue: rpi3
                  image: https://cdimage.ubuntu.com/pi.img.xz
                  workers: 2
                  username: user
                  password: pass

            # Allocate mode with distro provisioning.
            - ubuntu-core20:
                  queue: murcia-3200
                  image: core20-latest-stable
                  username: ubuntu

            # Reserve mode: key is imported; SSH credentials used to connect.
            - ubuntu-reserved:
                  queue: reserved-pool
                  reserve-key: lp:mylaunchpadid
                  ssh-rsa-key: '$(HOST: echo "$SPREAD_SSH_KEY")'

            # Full provision-data for a MAAS connector.
            - ubuntu-baremetal:
                  queue: baremetal-pool
                  provision-data:
                      distro: jammy
                      kernel: hwe-22.04
```
