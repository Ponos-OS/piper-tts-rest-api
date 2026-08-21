import { Client as MinioClient } from "minio";
import { randomUUID } from "node:crypto";
import { resolve } from "node:path";
import {
  GenericContainer,
  Network,
  type StartedNetwork,
  type StartedTestContainer,
  Wait,
} from "testcontainers";

const minioAccessKey = "minioadmin";
const minioSecretKey = "minioadmin";
const bucket = "synthesize-e2e";

export interface SynthesizeE2eJob {
  genUploadUrl: string;
  objectName: string;
  statusCallbackUrl: string;
}

export interface SynthesizeE2eStack {
  callbacks(job: SynthesizeE2eJob): Promise<WireMockRequest[]>;
  objectSize(job: SynthesizeE2eJob): Promise<number>;
  prepareJob(): Promise<SynthesizeE2eJob>;
  stop(): Promise<void>;
  submit(job: SynthesizeE2eJob, text: string): Promise<{ jobId: string }>;
}

export interface WireMockRequest {
  request: { body: string; method: string; url: string };
}

export async function startSynthesizeE2eStack(): Promise<SynthesizeE2eStack> {
  const containers: StartedTestContainer[] = [];
  const network = await new Network().start();

  try {
    const minio = await startMinio(network, containers);
    const minioClient = await startMinioClient(network, containers);
    await run(
      minioClient,
      "mc alias set local http://minio:9000 minioadmin minioadmin",
    );
    await run(minioClient, `mc mb local/${bucket}`);

    const wiremock = await new GenericContainer("wiremock/wiremock:3.13.1")
      .withNetwork(network)
      .withNetworkAliases("wiremock")
      .withExposedPorts(8080)
      .withWaitStrategy(
        Wait.forHttp("/__admin/mappings", 8080).forStatusCode(200),
      )
      .start();
    containers.push(wiremock);
    const wiremockUrl = `http://${wiremock.getHost()}:${wiremock.getMappedPort(8080)}`;

    await startTelemetry(network, containers);
    await startRabbitmq(network, containers);
    const backend = await startBackend(network, containers);

    return {
      callbacks: async (job) => {
        const response = await fetch(`${wiremockUrl}/__admin/requests`);
        const payload = (await response.json()) as {
          requests: WireMockRequest[];
        };
        return payload.requests.filter(
          (request) =>
            request.request.method === "POST" &&
            request.request.url === new URL(job.statusCallbackUrl).pathname,
        );
      },
      objectSize: async (job) => {
        const output = await run(
          minioClient,
          `mc stat --json local/${bucket}/${job.objectName}`,
        );
        const size = (JSON.parse(output) as { size: number }).size;

        return size;
      },
      prepareJob: async () => {
        const id = randomUUID();
        const objectName = `${id}.mp3`;
        const uploadUrl = await new MinioClient({
          endPoint: minio.getIpAddress(network.getName()),
          port: 9000,
          useSSL: false,
          accessKey: minioAccessKey,
          secretKey: minioSecretKey,
        }).presignedPutObject(bucket, objectName, 600);
        const genUploadUrl = `http://wiremock:8080/upload-url/${id}`;
        const statusCallbackUrl = `http://wiremock:8080/status/${id}`;
        await Promise.all([
          stub(wiremockUrl, {
            request: { method: "POST", urlPath: `/upload-url/${id}` },
            response: {
              status: 200,
              headers: { "Content-Type": "application/json" },
              jsonBody: { uploadUrl },
            },
          }),
          stub(wiremockUrl, {
            request: { method: "POST", urlPath: `/status/${id}` },
            response: { status: 204 },
          }),
        ]);
        return { genUploadUrl, objectName, statusCallbackUrl };
      },
      stop: async () => {
        await Promise.allSettled(
          containers.reverse().map((container) => container.stop()),
        );
        await network.stop();
      },
      submit: async (job, text) => {
        const response = await fetch(
          `http://${backend.getHost()}:${backend.getMappedPort(3000)}/synthesize`,
          {
            method: "POST",
            headers: { "content-type": "application/json" },
            body: JSON.stringify({ text, ...job }),
          },
        );
        if (response.status !== 202) {
          throw new Error(`Expected 202, received ${response.status}`);
        }
        return (await response.json()) as { jobId: string };
      },
    };
  } catch (error) {
    await Promise.allSettled(
      containers.reverse().map((container) => container.stop()),
    );
    await network.stop();
    throw error;
  }
}

async function startMinio(
  network: StartedNetwork,
  containers: StartedTestContainer[],
): Promise<StartedTestContainer> {
  const container = await new GenericContainer("minio/minio:latest")
    .withEnvironment({
      MINIO_ROOT_USER: minioAccessKey,
      MINIO_ROOT_PASSWORD: minioSecretKey,
    })
    .withCommand(["server", "/data", "--console-address", ":9001"])
    .withNetwork(network)
    .withNetworkAliases("minio")
    .withExposedPorts(9000)
    .withWaitStrategy(Wait.forListeningPorts())
    .start();
  containers.push(container);
  return container;
}

async function startMinioClient(
  network: StartedNetwork,
  containers: StartedTestContainer[],
): Promise<StartedTestContainer> {
  const container = await new GenericContainer("minio/mc:latest")
    .withEntrypoint(["/bin/sh", "-c"])
    .withCommand(["sleep infinity"])
    .withNetwork(network)
    .start();
  containers.push(container);
  return container;
}

async function startTelemetry(
  network: StartedNetwork,
  containers: StartedTestContainer[],
): Promise<void> {
  const tempo = await new GenericContainer("grafana/tempo:2.8.2")
    .withCommand(["-config.file=/etc/tempo.yaml"])
    .withCopyFilesToContainer([
      {
        source: resolve("local-setup/tempo.yaml"),
        target: "/etc/tempo.yaml",
      },
    ])
    .withNetwork(network)
    .withNetworkAliases("tempo")
    .withExposedPorts(4317)
    .withWaitStrategy(Wait.forLogMessage("Tempo started"))
    .start();
  containers.push(tempo);

  const loki = await new GenericContainer("grafana/loki:3.6.2")
    .withCommand(["-config.file=/etc/loki.yaml"])
    .withCopyFilesToContainer([
      {
        source: resolve("local-setup/loki.yaml"),
        target: "/etc/loki.yaml",
      },
    ])
    .withNetwork(network)
    .withNetworkAliases("loki")
    .withExposedPorts(3100)
    .withWaitStrategy(Wait.forLogMessage("server listening on addresses"))
    .start();
  containers.push(loki);

  const collector = await new GenericContainer(
    "otel/opentelemetry-collector-contrib:0.155.0",
  )
    .withCommand(["--config=/etc/otel-collector-config.yaml"])
    .withCopyFilesToContainer([
      {
        source: resolve("local-setup/otel-collector-config.yaml"),
        target: "/etc/otel-collector-config.yaml",
      },
    ])
    .withNetwork(network)
    .withNetworkAliases("otel-collector")
    .withExposedPorts(13133)
    .withWaitStrategy(Wait.forHttp("/", 13133).forStatusCode(200))
    .start();
  containers.push(collector);
}

async function startRabbitmq(
  network: StartedNetwork,
  containers: StartedTestContainer[],
): Promise<void> {
  const container = await new GenericContainer("rabbitmq:4.3-management-alpine")
    .withNetwork(network)
    .withNetworkAliases("rabbitmq")
    .withExposedPorts(5672)
    .withWaitStrategy(Wait.forListeningPorts())
    .start();
  containers.push(container);
}

async function startBackend(
  network: StartedNetwork,
  containers: StartedTestContainer[],
): Promise<StartedTestContainer> {
  const image = await GenericContainer.fromDockerfile(resolve(".")).build(
    "piper-tts-rest-api-e2e",
    { deleteOnExit: false },
  );
  const container = await image
    .withEnvironment({
      NODE_ENV: "test",
      RABBITMQ_URL: "amqp://rabbitmq:5672",
      OTEL_ENABLED: "true",
      OTEL_EXPORTER_OTLP_ENDPOINT: "http://otel-collector:4318",
    })
    .withNetwork(network)
    .withNetworkAliases("backend")
    .withExposedPorts(3000)
    .withWaitStrategy(Wait.forHttp("/health", 3000).forStatusCode(200))
    .withStartupTimeout(120_000)
    .start();
  containers.push(container);
  return container;
}

async function stub(mappingUrl: string, mapping: unknown): Promise<void> {
  const response = await fetch(`${mappingUrl}/__admin/mappings`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify(mapping),
  });
  if (!response.ok) {
    throw new Error(`Unable to create WireMock stub: ${response.status}`);
  }
}

async function run(
  container: StartedTestContainer,
  command: string,
): Promise<string> {
  const result = await container.exec(["sh", "-c", command]);
  if (result.exitCode !== 0) {
    throw new Error(result.output);
  }
  return result.output.trim();
}

export async function waitFor(
  condition: () => boolean | Promise<boolean>,
  timeoutMs = 30_000,
): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (await condition()) {
      return;
    }
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  throw new Error("Timed out waiting for synthesis completion");
}

export function statusOf(request: WireMockRequest): string {
  return (JSON.parse(request.request.body) as { status: string }).status;
}

export function jobIdOf(request: WireMockRequest): string {
  return (JSON.parse(request.request.body) as { jobId: string }).jobId;
}
