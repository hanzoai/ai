<!-- markdownlint-disable MD030 -->

# ai
### Build LLM Apps Easily

[![Release Notes](https://img.shields.io/github/release/hanzoai/ai)](https://github.com/hanzoai/ai/releases)
[![Twitter Follow](https://img.shields.io/twitter/follow/HanzoAI?style=social)](https://twitter.com/HanzoAI)
[![GitHub star chart](https://img.shields.io/github/stars/HanzoAI/Hanzo?style=social)](https://star-history.com/#hanzoai/ai)
[![GitHub fork](https://img.shields.io/github/forks/HanzoAI/Hanzo?style=social)](https://github.com/hanzoai/ai/fork)

English | [中文](./i18n/README-ZH.md) | [日本語](./i18n/README-JA.md) | [한국어](./i18n/README-KR.md)

<h3>Drag & drop UI to build your customized LLM flow</h3>
<a href="https://github.com/hanzoai/ai">
<img width="100%" src="https://github.com/hanzoai/ai/blob/main/images/hanzo.gif?raw=true"></a>

## ⚡Quick Start

Download and Install [NodeJS](https://nodejs.org/en/download) >= 18.15.0

1. Install Hanzo
    ```bash
    npm install -g @hanzo/ai
    ```
2. Start Hanzo

    ```bash
    npx @hanzo/ai start
    ```

    With username & password

    ```bash
    npx hanzo start --HANZO_USERNAME=user --HANZO_PASSWORD=1234
    ```

3. Open [http://localhost:3000](http://localhost:3000)

## 🐳 Docker

### Docker Compose

1. Go to `docker` folder at the root of the project
2. Copy `.env.example` file, paste it into the same location, and rename to `.env`
3. `docker compose up -d`
4. Open [http://localhost:3000](http://localhost:3000)
5. You can bring the containers down by `docker compose stop`

### Docker Image

1. Build the image locally:
    ```bash
    docker build --no-cache -t hanzo .
    ```
2. Run image:

    ```bash
    docker run -d --name hanzo -p 3000:3000 hanzo
    ```

3. Stop image:
    ```bash
    docker stop hanzo
    ```

## 👨‍💻 Developers

Hanzo AI has 3 different modules in a single mono repository.

-   `server`: Node backend to serve API logics
-   `ui`: React frontend
-   `components`: Third-party nodes integrations

### Prerequisite

-   Install [PNPM](https://pnpm.io/installation)
    ```bash
    npm i -g pnpm
    ```

### Setup

1. Clone the repository

    ```bash
    git clone https://github.com/hanzoai/ai.git
    ```

2. Go into repository folder

    ```bash
    cd ai
    ```

3. Install all dependencies of all modules:

    ```bash
    pnpm install
    ```

4. Build all the code:

    ```bash
    pnpm build
    ```

5. Start the app:

    ```bash
    pnpm start
    ```

    You can now access the app on [http://localhost:3000](http://localhost:3000)

6. For development build:

    - Create `.env` file and specify the `VITE_PORT` (refer to `.env.example`) in `packages/ui`
    - Create `.env` file and specify the `PORT` (refer to `.env.example`) in `packages/server`
    - Run

        ```bash
        pnpm dev
        ```

    Any code changes will reload the app automatically on [http://localhost:8080](http://localhost:8080)

## 🔒 Authentication

To enable app level authentication, add `HANZO_USERNAME` and `HANZO_PASSWORD` to the `.env` file in `packages/server`:

```
HANZO_USERNAME=user
HANZO_PASSWORD=1234
```

## 🌱 Env Variables

Hanzo support different environment variables to configure your instance. You can specify the following variables in the `.env` file inside `packages/server` folder. Read [more](https://github.com/hanzoai/ai/blob/main/CONTRIBUTING.md#-env-variables)

## 📖 Documentation

[Hanzo Docs](https://docs.hanzo.ai/)

## 🌐 Self Host

Deploy Hanzo AI self-hosted in your existing infrastructure, we support various [deployments](https://docs.hanzo.ai/configuration/deployment)

-   [AWS](https://docs.hanzo.ai/deployment/aws)
-   [Azure](https://docs.hanzo.ai/deployment/azure)
-   [Digital Ocean](https://docs.hanzo.ai/deployment/digital-ocean)
-   [GCP](https://docs.hanzo.ai/deployment/gcp)
-   <details>
      <summary>Others</summary>

    -   [Railway](https://docs.hanzo.ai/deployment/railway)

        [![Deploy on Railway](https://railway.app/button.svg)](https://railway.app/template/pn4G8S?referralCode=WVNPD9)

    -   [Render](https://docs.hanzo.ai/deployment/render)

        [![Deploy to Render](https://render.com/images/deploy-to-render-button.svg)](https://docs.hanzo.ai/deployment/render)

    -   [HuggingFace Spaces](https://docs.hanzo.ai/deployment/hugging-face)

        <a href="https://huggingface.co/spaces/HanzoAI/Hanzo"><img src="https://huggingface.co/datasets/huggingface/badges/raw/main/open-in-hf-spaces-sm.svg" alt="HuggingFace Spaces"></a>

    -   [Elestio](https://elest.io/open-source/hanzoai)

        [![Deploy](https://pub-da36157c854648669813f3f76c526c2b.r2.dev/deploy-on-elestio-black.png)](https://elest.io/open-source/hanzoai)

    -   [Sealos](https://cloud.sealos.io/?openapp=system-template%3FtemplateName%3Dhanzo)

        [![](https://raw.githubusercontent.com/labring-actions/templates/main/Deploy-on-Sealos.svg)](https://cloud.sealos.io/?openapp=system-template%3FtemplateName%3Dhanzo)

    -   [RepoCloud](https://repocloud.io/details/?app_id=29)

        [![Deploy on RepoCloud](https://d16t0pc4846x52.cloudfront.net/deploy.png)](https://repocloud.io/details/?app_id=29)

      </details>

## 💻 Cloud Hosted

For a robust and scalable cloud hosted version, use [Hanzo AI](https://hanzo.ai).

## 🙋 Support

Feel free to ask any questions, raise problems, and request new features in [discussion](https://github.com/hanzoai/ai/discussions)

## 🙌 Contributing

Thanks go to these awesome contributors

<a href="https://github.com/hanzoai/ai/graphs/contributors">
<img src="https://contrib.rocks/image?repo=hanzoai/ai" />
</a>

See [contributing guide](CONTRIBUTING.md). Reach out to us via [Email](mailto:hi@hanzo.ai) if you have any questions or issues.

## 📄 License

Source code in this repository is made available under the [Apache License Version 2.0](LICENSE.md).
