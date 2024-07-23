# Hanzo Docker Hub Image

Starts Hanzo from [DockerHub Image](https://hub.docker.com/r/hanzoai/hanzo)

## Usage

1. Create `.env` file and specify the `PORT` (refer to `.env.example`)
2. `docker compose up -d`
3. Open [http://localhost:3000](http://localhost:3000)
4. You can bring the containers down by `docker compose stop`

## 🔒 Authentication

1. Create `.env` file and specify the `PORT`, `HANZO_USERNAME`, and `HANZO_PASSWORD` (refer to `.env.example`)
2. Pass `HANZO_USERNAME` and `HANZO_PASSWORD` to the `docker-compose.yml` file:
    ```
    environment:
        - PORT=${PORT}
        - HANZO_USERNAME=${HANZO_USERNAME}
        - HANZO_PASSWORD=${HANZO_PASSWORD}
    ```
3. `docker compose up -d`
4. Open [http://localhost:3000](http://localhost:3000)
5. You can bring the containers down by `docker compose stop`

## 🌱 Env Variables

If you like to persist your data (flows, logs, apikeys, credentials), set these variables in the `.env` file inside `docker` folder:

-   DATABASE_PATH=/root/.hanzo
-   APIKEY_PATH=/root/.hanzo
-   LOG_PATH=/root/.hanzo/logs
-   SECRETKEY_PATH=/root/.hanzo
-   BLOB_STORAGE_PATH=/root/.hanzo/storage

Hanzo also support different environment variables to configure your instance. Read [more](https://docs.hanzo.ai/environment-variables)
