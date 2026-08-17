# CutScene

CutScene lets you create short clips from media that is currently playing in Plex. It includes a browser UI and an HTTP API.

![](.github/cutscene.png)

## Setup

Copy [config.example.yaml](config.example.yaml) to [config.yaml](config.yaml) in the same directory and set the values for your Plex server. The Plex token is used by CutScene to read server metadata; see [Plex's instructions for finding an authentication token](https://support.plex.tv/articles/204059436-finding-an-authentication-token-x-plex-token/).

Set `api.domain` to the browser-reachable base URL for CutScene. It is passed to Plex as the OAuth/PIN authentication forward URL, so it must be the URL users can return to after signing in (not a private container hostname).

The default [docker-compose.yaml](docker-compose.yaml) uses the [GitHub Container Registry image](https://github.com/ahornerr/CutScene/pkgs/container/cutscene). Start it with:

```sh
docker compose up
```

If you changed the listen address or port, update the port mapping in [docker-compose.yaml](docker-compose.yaml).

### Durable clips and storage

Completed renders are promoted to durable clips. Configure `storage.root` (the
sample uses `/data`) as the local application-data directory. CutScene stores
the SQLite clip metadata database there, MP4s below `clips/`, and the
`clip-token.key` encryption key. The default Compose file mounts the named
`cutscene-data` volume at `/data`; it deliberately does not mount `/tmp`, which
contains transient render jobs and may be cleared on restart.

If `storage.root` is omitted it defaults to `./data` relative to the CutScene
process working directory. If `storage.database` is omitted it defaults to
`clips.sqlite3` directly below that root; configured database paths must remain
relative to the root.

Do not use `docker compose down -v` unless you intend to delete the durable
volume and all saved clips.

The storage root and clip directory are created with owner-only permissions;
the root and clip directory use `0700`, and `clip-token.key` must be exactly
`0600`. Restrict access to that key as you would any capability secret. Keep
the database, MP4s, and key in the same encrypted backup set. Before upgrading
from an older release, take a complete encrypted backup of the storage root;
the key is required to preserve encrypted share links. Backups should be taken
with CutScene stopped (or with a filesystem/database snapshot) so metadata and
files represent one point in time. If the key is missing or wrong, startup
refuses to run until the matching key is restored; it does not generate a
replacement key. The MP4s may remain, but the service will not expose or serve
encrypted share links without that matching key.

To use a host path or a custom volume, set `storage.root` and mount that exact
path into the container. For example, with `storage.root: /srv/cutscene`, use
`/host/cutscene:/srv/cutscene` rather than mounting `/data`; the database path
is relative to the root by default and absolute or escaping paths are rejected.
Startup validates the restored schema, key, encrypted tokens, and clip files.
A missing database, missing/wrong key, or ambiguous restore fails closed and
leaves existing MP4s untouched; restore the matching backup and retry.

Durable clip capacity is intentionally unbounded by application policy. Monitor
the mounted filesystem and plan disk alerts/backups; the existing queue,
per-owner, FFmpeg, and transient render limits still apply.

On startup, CutScene preflights clip metadata and files before any migration
or reconciliation. Mismatched, stale, partial, or unreferenced durable state
fails closed without deleting either side. A legacy Phase 1 hash-only database
is migrated to encrypted token storage; because the old
raw token was not retained, those legacy clips receive newly generated share
tokens while their metadata and MP4s are preserved. Deleting a clip is a hard
delete of both its SQLite metadata and MP4 bytes and is permanent.

### Authentication

Open the CutScene URL in a browser and choose **Log in with Plex**. CutScene uses Plex's browser authentication flow and keeps the authenticated session in the browser. The session, preview, and render endpoints require that authenticated session; an HTTP client must preserve the session cookie established by its Plex login.

### Hardware acceleration

The default `libx264` codec uses software encoding. On Linux, set `ffmpeg.codec` to `h264_vaapi` for VAAPI hardware encoding. The host and container need access to a usable render device under `/dev/dri`; the supplied [docker-compose.gpu.yaml](docker-compose.gpu.yaml) mounts `/dev/dri/renderD128`:

```sh
docker compose -f docker-compose.yaml -f docker-compose.gpu.yaml up
```

The VAAPI setup is tested with AMD GPUs on Linux. Intel QuickSync through VAAPI may work but is untested. `h264_nvenc` is also available for Nvidia, but requires a working Nvidia driver, the [Nvidia Container Toolkit](https://github.com/NVIDIA/nvidia-container-toolkit), and Docker configured to expose the GPU; the supplied GPU override only provides the VAAPI/DRI device.

## Usage

### Browser

After authentication, choose an active Plex session in the UI, preview and trim it, then submit the render. Completed clips are downloadable from the render-job status panel.

### HTTP render jobs

The examples below assume an authenticated session cookie in `$COOKIE`:

```sh
BASE=http://127.0.0.1:8080
curl -sS -b "$COOKIE" "$BASE/sessions"
```

Use an active session's `ratingKey` and media/part ID as `mediaId` to create a job. The request must be JSON:

```sh
curl -i -b "$COOKIE" \
  -H 'Content-Type: application/json' \
  -d '{
    "ratingKey": "100151",
    "mediaId": 123456,
    "fromMs": 300000,
    "toMs": 305000,
    "subtitleIndex": -1,
    "height": 720,
    "qp": 24,
    "audioMode": "standard"
  }' \
  "$BASE/render-jobs"
```

Creation returns `202 Accepted`, a JSON job object, and a `Location` header such as `/render-jobs/<id>`. Poll that URL until `status` is `succeeded` or `failed` (or the job expires):

```sh
curl -sS -b "$COOKIE" "$BASE/render-jobs/<id>"
curl -fL -b "$COOKIE" "$BASE/render-jobs/<id>/download" -o clip.mp4
```

Jobs are private to the authenticated user. A successful output is temporary and normally expires within one hour; use `expiresAt` and download it before expiry. An expired job or download returns `410 Gone`.

If render capacity or temporary service/storage capacity is unavailable, creation can return `429` or `503` with a `Retry-After` header. Wait for that interval before retrying rather than submitting a tight loop.

#### Render options

- `subtitleIndex`: `-1` for no subtitles, or the selected subtitle index.
- `height`: output height in pixels; `0` keeps the source height. Valid explicit heights are 144–2160 and even.
- `qp`: encoder quantization parameter, `0` for the encoder default, or a value from 0–51.
- `audioMode`: `standard` (original audio), `dialogue` (centred-dialogue boost), or `dialogue_normalized` (dialogue boost plus loudness normalization). If omitted, it defaults to `standard`.

The selected range must be ordered, non-negative, and no longer than 15 minutes.

`ffmpeg.concurrency` controls the maximum number of concurrent FFmpeg processes. It defaults to `2` when omitted or non-positive, and the limit is shared by previews, subtitle preparation, and render jobs.

### Semantic-search proof of concept

CutScene includes an optional semantic-search proof of concept with shared pgvector PostgreSQL infrastructure and one GPU embedding backend. Choose exactly one mutually exclusive overlay:

- NVIDIA: Hugging Face Text Embeddings Inference (TEI), `BAAI/bge-base-en-v1.5`, service `tei`.
- AMD: vLLM ROCm, `BAAI/bge-base-en-v1.5`, service `embeddings`.

Neither PostgreSQL nor the embedding service publishes a host port; CutScene reaches both over Compose's private network. The shared [docker-compose.semantic-search.yaml](docker-compose.semantic-search.yaml) contains PostgreSQL only. Do not combine the NVIDIA and AMD overlays.

#### NVIDIA prerequisites and startup

Install the [NVIDIA Container Toolkit](https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/latest/install-guide.html), configure Docker with it, and verify both the host and Docker GPU paths:

```sh
nvidia-smi
docker run --rm --gpus all nvidia/cuda:12.4.1-base-ubuntu22.04 nvidia-smi
```

If the Docker check cannot see the GPU, configure the runtime with `sudo nvidia-ctk runtime configure --runtime=docker` and restart Docker, then repeat the check. The default TEI tag, `86-1.9`, targets NVIDIA Ampere hardware such as RTX 30-series cards and A10s. TEI CUDA tags and supported GPU architectures change over time; select a compatible tag in [docker-compose.semantic-search.nvidia.yaml](docker-compose.semantic-search.nvidia.yaml) for another architecture.

Use the NVIDIA configuration in [config.example.yaml](config.example.yaml): `embeddings_provider: tei` and `embeddings_url: http://tei:80`. Start exactly this combination:

```sh
cp config.example.yaml config.yaml
# edit config.yaml: set plex.host, plex.token, and api.domain
docker compose -f docker-compose.yaml -f docker-compose.semantic-search.yaml -f docker-compose.semantic-search.nvidia.yaml up -d
docker compose -f docker-compose.yaml -f docker-compose.semantic-search.yaml -f docker-compose.semantic-search.nvidia.yaml ps
```

#### AMD prerequisites and startup

Install a ROCm release compatible with the host GPU and verify the host and container device paths:

```sh
rocminfo
rocm-smi
docker run --rm --device=/dev/kfd --device=/dev/dri rocm/pytorch:latest rocminfo
```

If the container check cannot see the GPU, ensure `/dev/kfd` and `/dev/dri` exist, the invoking user can access their `render`/`video` groups, and Docker is allowed to pass those devices. Some AMD architectures require an `HSA_OVERRIDE_GFX_VERSION`; set that architecture-specific value in [docker-compose.semantic-search.amd.yaml](docker-compose.semantic-search.amd.yaml) only when required by the installed ROCm release.

For AMD, change the sample configuration to `embeddings_provider: openai` and `embeddings_url: http://embeddings:8000`. The AMD overlay passes the model as vLLM's positional argument and explicitly selects its pooling/embed runner; this is required by current vLLM images. Start exactly this mutually exclusive combination:

```sh
cp config.example.yaml config.yaml
# edit config.yaml: set Plex values and change semantic_search to the AMD values
docker compose -f docker-compose.yaml -f docker-compose.semantic-search.yaml -f docker-compose.semantic-search.amd.yaml up -d
docker compose -f docker-compose.yaml -f docker-compose.semantic-search.yaml -f docker-compose.semantic-search.amd.yaml ps
```

Wait for `postgres` and the selected embedding service (`tei` for NVIDIA or `embeddings` for AMD) to report `healthy` before indexing. The model loads during startup, so the embedding healthcheck can take a while. For example, inspect the shared database health with `pg_isready -U cutscene -d cutscene` in the `postgres` container and inspect the selected service with its `/health` endpoint. The sample `semantic_search.postgres_dsn` uses the internal POC service name and a non-production password; keep it synchronized with `POSTGRES_PASSWORD` and do not reuse it outside this local setup.

#### Move the semantic-search database from a desktop to a NAS

Use a PostgreSQL logical dump rather than copying the Docker volume. The procedure below preserves the pgvector tables and embeddings while allowing the NAS to use its own Docker storage. Run the commands in Bash, and replace the angle-bracket placeholders. Do not put passwords, Plex tokens, or session cookies in commands.

1. **Stop writes before exporting.** Let any in-flight indexing request finish, do not start another one, and stop CutScene so no new index writes can begin. Keep using the same mutually exclusive overlay that is currently selected:

   ```sh
   # Choose exactly one:
   COMPOSE=(docker compose -f docker-compose.yaml -f docker-compose.semantic-search.yaml -f docker-compose.semantic-search.nvidia.yaml)
   # AMD alternative:
   # COMPOSE=(docker compose -f docker-compose.yaml -f docker-compose.semantic-search.yaml -f docker-compose.semantic-search.amd.yaml)

   "${COMPOSE[@]}" stop cutscene
   ```

   If another process can write to the `postgres` service, stop or pause it too. The stopped application is the POC's indexing pause; do not export while indexing is active.

2. **Create a custom-format dump from the source `postgres` container.** The shared Compose service is named `postgres`; `pg_dump -Fc` writes a compressed, restorable custom-format archive. The database and user are read from the container's Compose environment, so no credential is included in the command:

   ```sh
   "${COMPOSE[@]}" exec -T postgres sh -lc 'pg_dump -Fc -U "$POSTGRES_USER" -d "$POSTGRES_DB"' > semantic-search.dump
   chmod 600 semantic-search.dump
   ```

   Check that the dump file is non-empty and keep it on encrypted, access-controlled storage during the transfer. Do not delete the source database until the NAS restore and verification succeed.

3. **Securely copy the dump to the NAS.** Use an already configured SSH key or another approved secure SSH mechanism; the placeholders below are not credentials:

   ```sh
   scp -p semantic-search.dump <nas-user>@<nas-host>:<nas-secure-directory>/semantic-search.dump
   # Or, for a resumable transfer:
   rsync -a --chmod=F600 semantic-search.dump <nas-user>@<nas-host>:<nas-secure-directory>/semantic-search.dump
   ```

4. **Prepare PostgreSQL on the NAS.** Place the repository Compose files on the NAS and use the same `docker-compose.semantic-search.yaml`. It currently pins `pgvector/pgvector:pg16`; start the same pgvector/PostgreSQL major version on the NAS before restoring. Use the same selected overlay (NVIDIA or AMD) if the NAS will run CutScene and embeddings, and never combine both overlays. Start only the database first:

   ```sh
   # Use the matching NVIDIA or AMD COMPOSE assignment from step 1.
   "${COMPOSE[@]}" up -d postgres
   "${COMPOSE[@]}" ps postgres
   ```

   Confirm that the `postgres` container is healthy. Do not start CutScene until the restore and configuration checks below are complete. If the NAS Compose files use a different PostgreSQL/pgvector major version, stop and use the source major version first; perform a separate PostgreSQL upgrade after migration.

5. **Restore the archive on the NAS.** Copy the archive into the NAS `postgres` container, then restore into the existing Compose database. `--clean --if-exists` removes objects present in the archive before recreating them; `--no-owner` avoids requiring source host role IDs.

   ```sh
   "${COMPOSE[@]}" cp <nas-secure-directory>/semantic-search.dump postgres:/tmp/semantic-search.dump
   "${COMPOSE[@]}" exec -T postgres sh -lc 'pg_restore --clean --if-exists --no-owner --exit-on-error -U "$POSTGRES_USER" -d "$POSTGRES_DB" /tmp/semantic-search.dump'
   "${COMPOSE[@]}" exec -T postgres rm -f /tmp/semantic-search.dump
   ```

6. **Verify the extension, table, and row count.** These checks use the current schema's `vector` extension and `subtitle_chunks` table. The count should match the source count (record the source count before migration if an exact comparison is needed):

   ```sh
   "${COMPOSE[@]}" exec -T postgres sh -lc 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -c "SELECT extname, extversion FROM pg_extension WHERE extname = '\''vector'\'';"'
   "${COMPOSE[@]}" exec -T postgres sh -lc 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -c "SELECT to_regclass('\''public.subtitle_chunks'\'') AS subtitle_chunks;"'
   "${COMPOSE[@]}" exec -T postgres sh -lc 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -c "SELECT count(*) AS subtitle_chunk_count FROM public.subtitle_chunks;"'
   ```

   A missing `vector` extension, a null `subtitle_chunks` relation, or an unexpected count means the migration is not verified. Keep the source dump and investigate before starting indexing on the NAS.

7. **Update and verify the NAS CutScene configuration.** Ensure the NAS `config.yaml` points to the internal NAS service names, not `localhost`: use `postgres` in `semantic_search.postgres_dsn`, `tei` with `embeddings_provider: tei` for the NVIDIA overlay, or `embeddings` with `embeddings_provider: openai` for the AMD overlay. Also ensure the NAS uses the same Plex server and owner identity/Plex account semantics as the desktop: preserve the corresponding owner account configuration and Plex access, rather than initializing the NAS against a different Plex owner or account. Keep Plex tokens in the protected NAS config file, never in shell history or migration commands. Start the full, matching Compose combination only after these checks pass, then leave indexing paused until a small search/index smoke test succeeds.

Moving the named `semantic-search-postgres-data` Docker volume directly is possible only with careful full-volume shutdown and compatible Docker storage paths, ownership, PostgreSQL major version, and host architecture. It is less portable, easier to make inconsistent, and harder to validate than `pg_dump -Fc`/`pg_restore`; it is not recommended for this migration.

#### Index and search

Sign in to CutScene, select one Plex library source, and use that source's `ratingKey`, `mediaId`, and `partId` for the synchronous index request. Preserve the authenticated session cookie in `$COOKIE`:

```sh
BASE=http://127.0.0.1:8080
# Select a source in the CutScene library UI and set these three source values.
RATING_KEY='<selected-source-rating-key>'
MEDIA_ID='<selected-source-media-id>'
PART_ID='<selected-source-part-id>'
curl -i -b "$COOKIE" -H 'Content-Type: application/json' \
  -d "{\"ratingKey\":\"$RATING_KEY\",\"mediaId\":$MEDIA_ID,\"partId\":$PART_ID}" \
  -X POST "$BASE/subtitle-search/index"
```

Indexing is synchronous in this POC; keep the request open until it completes. Search the indexed source with:

```sh
curl -sS -b "$COOKIE" \
  "$BASE/subtitle-search?q=$(python3 -c 'import urllib.parse,sys; print(urllib.parse.quote(sys.argv[1]))' 'a line to find')"
```

The POC embeds English text only. Plex must be reachable from the CutScene container over HTTP/HTTPS so subtitle media can be read. PGS/image subtitles and external subtitle tracks are excluded. Only one source is indexed per synchronous request; this is not yet a background or multi-source indexing system.

## Development

The [docker-compose.build.yaml](docker-compose.build.yaml) file builds the Docker image from source:

```sh
docker compose -f docker-compose.yaml -f docker-compose.build.yaml up --build
```

Alternatively, compile or run directly with `go build ./...` or `go run ./...` (Go >= 1.22 recommended).
