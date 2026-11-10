# Scaling guide

Scale worker replicas from RabbitMQ work demand; the application does **not** perform its own autoscaling. Each replica consumes with `prefetch=1`, so begin by treating one ready replica as one active synthesis pipeline.

## KEDA

Use the RabbitMQ scaler against `synthesize.work`. Choose queue length from measured generation time and the response-time target, then set conservative minimum and maximum replicas.

```yaml
triggers:
  - type: rabbitmq
    metadata:
      queueName: synthesize.work
      mode: QueueLength
      value: "2"
```

The example asks KEDA to add capacity around two queued jobs per replica; calibrate it with production generation duration, CPU, and memory measurements. See the [KEDA RabbitMQ scaler documentation](https://keda.sh/docs/latest/scalers/rabbitmq-queue/).

## Guardrails

- Set CPU and memory requests/limits from a full Piper/ffmpeg pipeline, not HTTP-only load.
- Keep `prefetch=1`. Raising it lets one worker reserve work it cannot synthesize concurrently and obscures queue depth.
- Set a non-zero `minReplicaCount` when cold-start latency is unacceptable. Scale-to-zero increases the first job's queue wait and requires RabbitMQ, model storage, and the workload identity to be ready before processing starts.
- Bound `maxReplicaCount` by model-download capacity, CPU quota, memory, callback/storage capacity, and RabbitMQ connection limits.
- Alert on DLQ growth, retry rate, queue age, and sustained unacknowledged messages before adding replicas blindly.

Retries consume queue capacity too. Delayed retries sit in the work queue until due, so its depth includes them.

## Rollouts and recovery

Use graceful termination periods long enough for the configured upload deadline or accept at-least-once reprocessing. A terminated worker can leave an unacknowledged message for redelivery; clients must tolerate duplicate callbacks and uploads.

Run RabbitMQ with persistent storage and quorum-queue replication appropriate to the failure domain. Verify broker recovery and DLQ handling as part of deployment testing.

### Shutdown and Stuck Requests

On SIGTERM or SIGINT the service stops accepting new connections and waits for in-flight requests (for example a long `/v1/speak` stream) to finish. It has no shutdown deadline of its own, so a stuck request, such as a client that stopped reading, keeps the process alive.

Your orchestrator must enforce the limit and force-kill the container:

- Kubernetes: set `terminationGracePeriodSeconds`; the kubelet sends SIGKILL when it expires (default 30s).
- Docker: `docker stop -t <seconds>` or `stop_grace_period` in Compose; Docker sends SIGKILL when it expires (default 10s).

A second SIGTERM or SIGINT makes the process exit immediately.
