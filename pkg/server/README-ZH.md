<!-- markdownlint-disable MD030 -->

# Hanzo - 低代码 LLM 应用程序构建器

[English](./README.md) | 中文

![Hanzo](https://github.com/HanzoAI/Hanzo/blob/main/images/hanzo.gif?raw=true)

拖放界面来构建自定义的 LLM 流程

## ⚡ 快速入门

1. 安装 Hanzo
    ```bash
    npm install -g hanzo
    ```
2. 启动 Hanzo

    ```bash
    npx hanzo start
    ```

3. 打开[http://localhost:3000](http://localhost:3000)

## 🔒 身份验证

要启用应用级身份验证，请将`HANZO_USERNAME`和`HANZO_PASSWORD`添加到`.env`文件中：

```
HANZO_USERNAME=user
HANZO_PASSWORD=1234
```

## 🌱 环境变量

Hanzo 支持不同的环境变量来配置您的实例。您可以在`packages/server`文件夹中的`.env`文件中指定以下变量。阅读[更多](https://docs.hanzo.ai/environment-variables)

您还可以在使用`npx`时指定环境变量。例如：

```
npx hanzo start --PORT=3000 --DEBUG=true
```

## 📖 文档

[Hanzo 文档](https://docs.hanzo.ai/)

## 🌐 自托管

### [Railway](https://docs.hanzo.ai/deployment/railway)

[![在Railway上部署](https://railway.app/button.svg)](https://railway.app/template/YK7J0v)

### [Render](https://docs.hanzo.ai/deployment/render)

[![部署到Render](https://render.com/images/deploy-to-render-button.svg)](https://docs.hanzo.ai/deployment/render)

### [AWS](https://docs.hanzo.ai/deployment/aws)

### [Azure](https://docs.hanzo.ai/deployment/azure)

### [DigitalOcean](https://docs.hanzo.ai/deployment/digital-ocean)

### [GCP](https://docs.hanzo.ai/deployment/gcp)

## 💻 云托管

即将推出

## 🙋 支持

在[讨论区](https://github.com/HanzoAI/Hanzo/discussions)中随时提出任何问题、报告问题和请求新功能。

## 🙌 贡献

请参阅[贡献指南](https://github.com/HanzoAI/Hanzo/blob/master/CONTRIBUTING.md)。如果您有任何问题或问题，请在[Discord](https://discord.gg/jbaHfsRVBW)上与我们联系。

## 📄 许可证

本仓库中的源代码在[Apache License Version 2.0 许可证](https://github.com/HanzoAI/Hanzo/blob/master/LICENSE.md)下提供。
