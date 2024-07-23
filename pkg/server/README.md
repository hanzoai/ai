<!-- markdownlint-disable MD030 -->

# Hanzo - Low-Code LLM apps builder

English | [中文](./README-ZH.md)

![Hanzo](https://github.com/HanzoAI/Hanzo/blob/main/images/hanzo.gif?raw=true)

Drag & drop UI to build your customized LLM flow

## ⚡Quick Start

1. Install Hanzo
    ```bash
    npm install -g hanzo
    ```
2. Start Hanzo

    ```bash
    npx hanzo start
    ```

3. Open [http://localhost:3000](http://localhost:3000)

## 🔒 Authentication

To enable app level authentication, add `HANZO_USERNAME` and `HANZO_PASSWORD` to the `.env` file:

```
HANZO_USERNAME=user
HANZO_PASSWORD=1234
```

## 🌱 Env Variables

Hanzo support different environment variables to configure your instance. You can specify the following variables in the `.env` file inside `packages/server` folder. Read [more](https://github.com/HanzoAI/Hanzo/blob/main/CONTRIBUTING.md#-env-variables)

You can also specify the env variables when using `npx`. For example:

```
npx hanzo start --PORT=3000 --DEBUG=true
```

## 📖 Tests

We use [Cypress](https://github.com/cypress-io) for our e2e testing. If you want to run the test suite in dev mode please follow this guide:

```sh
cd Hanzo/packages/server
pnpm install
./node_modules/.bin/cypress install
pnpm build
#Only for writting new tests on local dev -> pnpm run cypress:open
pnpm run e2e
```

## 📖 Documentation

[Hanzo Docs](https://docs.hanzo.ai/)

## 🌐 Self Host

### [Railway](https://docs.hanzo.ai/deployment/railway)

[![Deploy on Railway](https://railway.app/button.svg)](https://railway.app/template/YK7J0v)

### [Render](https://docs.hanzo.ai/deployment/render)

[![Deploy to Render](https://render.com/images/deploy-to-render-button.svg)](https://docs.hanzo.ai/deployment/render)

### [AWS](https://docs.hanzo.ai/deployment/aws)

### [Azure](https://docs.hanzo.ai/deployment/azure)

### [DigitalOcean](https://docs.hanzo.ai/deployment/digital-ocean)

### [GCP](https://docs.hanzo.ai/deployment/gcp)

## 💻 Cloud Hosted

Coming Soon

## 🙋 Support

Feel free to ask any questions, raise problems, and request new features in [discussion](https://github.com/HanzoAI/Hanzo/discussions)

## 🙌 Contributing

See [contributing guide](https://github.com/HanzoAI/Hanzo/blob/master/CONTRIBUTING.md). Reach out to us at [Discord](https://discord.gg/jbaHfsRVBW) if you have any questions or issues.

## 📄 License

Source code in this repository is made available under the [Apache License Version 2.0](https://github.com/HanzoAI/Hanzo/blob/master/LICENSE.md).
