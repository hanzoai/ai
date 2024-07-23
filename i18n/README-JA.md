<!-- markdownlint-disable MD030 -->

<img width="100%" src="https://github.com/HanzoAI/Hanzo/blob/main/images/hanzo.png?raw=true"></a>

# Hanzo - LLM アプリを簡単に構築

[![Release Notes](https://img.shields.io/github/release/HanzoAI/Hanzo)](https://github.com/HanzoAI/Hanzo/releases)
[![Discord](https://img.shields.io/discord/1087698854775881778?label=Discord&logo=discord)](https://discord.gg/jbaHfsRVBW)
[![Twitter Follow](https://img.shields.io/twitter/follow/HanzoAI?style=social)](https://twitter.com/HanzoAI)
[![GitHub star chart](https://img.shields.io/github/stars/HanzoAI/Hanzo?style=social)](https://star-history.com/#HanzoAI/Hanzo)
[![GitHub fork](https://img.shields.io/github/forks/HanzoAI/Hanzo?style=social)](https://github.com/HanzoAI/Hanzo/fork)

[English](../README.md) | [中文](./README-ZH.md) | 日本語 | [한국어](./README-KR.md)

<h3>ドラッグ＆ドロップでカスタマイズした LLM フローを構築できる UI</h3>
<a href="https://github.com/HanzoAI/Hanzo">
<img width="100%" src="https://github.com/HanzoAI/Hanzo/blob/main/images/hanzo.gif?raw=true"></a>

## ⚡ クイックスタート

[NodeJS](https://nodejs.org/en/download) >= 18.15.0 をダウンロードしてインストール

1. Hanzo のインストール
    ```bash
    npm install -g hanzo
    ```
2. Hanzo の実行

    ```bash
    npx hanzo start
    ```

    ユーザー名とパスワードを入力

    ```bash
    npx hanzo start --HANZO_USERNAME=user --HANZO_PASSWORD=1234
    ```

3. [http://localhost:3000](http://localhost:3000) を開く

## 🐳 Docker

### Docker Compose

1. プロジェクトのルートにある `docker` フォルダに移動する
2. `.env.example` ファイルをコピーして同じ場所に貼り付け、名前を `.env` に変更する
3. `docker compose up -d`
4. [http://localhost:3000](http://localhost:3000) を開く
5. コンテナを停止するには、`docker compose stop` を使用します

### Docker Image

1. ローカルにイメージを構築する:
    ```bash
    docker build --no-cache -t hanzo .
    ```
2. image を実行:

    ```bash
    docker run -d --name hanzo -p 3000:3000 hanzo
    ```

3. image を停止:
    ```bash
    docker stop hanzo
    ```

## 👨‍💻 開発者向け

Hanzo には、3 つの異なるモジュールが 1 つの mono リポジトリにあります。

-   `server`: API ロジックを提供する Node バックエンド
-   `ui`: React フロントエンド
-   `components`: サードパーティノードとの統合

### 必須条件

-   [PNPM](https://pnpm.io/installation) をインストール
    ```bash
    npm i -g pnpm
    ```

### セットアップ

1. リポジトリをクローン

    ```bash
    git clone https://github.com/HanzoAI/Hanzo.git
    ```

2. リポジトリフォルダに移動

    ```bash
    cd Hanzo
    ```

3. すべてのモジュールの依存関係をインストール:

    ```bash
    pnpm install
    ```

4. すべてのコードをビルド:

    ```bash
    pnpm build
    ```

5. アプリを起動:

    ```bash
    pnpm start
    ```

    [http://localhost:3000](http://localhost:3000) でアプリにアクセスできるようになりました

6. 開発用ビルド:

    - `.env` ファイルを作成し、`packages/ui` に `VITE_PORT` を指定する（`.env.example` を参照）
    - `.env` ファイルを作成し、`packages/server` に `PORT` を指定する（`.env.example` を参照）
    - 実行

        ```bash
        pnpm dev
        ```

    コードの変更は [http://localhost:8080](http://localhost:8080) に自動的にアプリをリロードします

## 🔒 認証

アプリレベルの認証を有効にするには、 `HANZO_USERNAME` と `HANZO_PASSWORD` を `packages/server` の `.env` ファイルに追加します:

```
HANZO_USERNAME=user
HANZO_PASSWORD=1234
```

## 🌱 環境変数

Hanzo は、インスタンスを設定するためのさまざまな環境変数をサポートしています。`packages/server` フォルダ内の `.env` ファイルで以下の変数を指定することができる。[続き](https://github.com/HanzoAI/Hanzo/blob/main/CONTRIBUTING.md#-env-variables)を読む

## 📖 ドキュメント

[Hanzo ドキュメント](https://docs.hanzo.ai/)

## 🌐 セルフホスト

お客様の既存インフラに Hanzo をセルフホストでデプロイ、様々な[デプロイ](https://docs.hanzo.ai/configuration/deployment)をサポートします

-   [AWS](https://docs.hanzo.ai/deployment/aws)
-   [Azure](https://docs.hanzo.ai/deployment/azure)
-   [Digital Ocean](https://docs.hanzo.ai/deployment/digital-ocean)
-   [GCP](https://docs.hanzo.ai/deployment/gcp)
-   <details>
      <summary>その他</summary>

    -   [Railway](https://docs.hanzo.ai/deployment/railway)

        [![Deploy on Railway](https://railway.app/button.svg)](https://railway.app/template/pn4G8S?referralCode=WVNPD9)

    -   [Render](https://docs.hanzo.ai/deployment/render)

        [![Deploy to Render](https://render.com/images/deploy-to-render-button.svg)](https://docs.hanzo.ai/deployment/render)

    -   [Hugging Face Spaces](https://docs.hanzo.ai/deployment/hugging-face)

        <a href="https://huggingface.co/spaces/HanzoAI/Hanzo"><img src="https://huggingface.co/datasets/huggingface/badges/raw/main/open-in-hf-spaces-sm.svg" alt="Hugging Face Spaces"></a>

    -   [Elestio](https://elest.io/open-source/hanzoai)

        [![Deploy](https://pub-da36157c854648669813f3f76c526c2b.r2.dev/deploy-on-elestio-black.png)](https://elest.io/open-source/hanzoai)

    -   [Sealos](https://cloud.sealos.io/?openapp=system-template%3FtemplateName%3Dhanzo)

        [![](https://raw.githubusercontent.com/labring-actions/templates/main/Deploy-on-Sealos.svg)](https://cloud.sealos.io/?openapp=system-template%3FtemplateName%3Dhanzo)

    -   [RepoCloud](https://repocloud.io/details/?app_id=29)

        [![Deploy on RepoCloud](https://d16t0pc4846x52.cloudfront.net/deploy.png)](https://repocloud.io/details/?app_id=29)

      </details>

## 💻 クラウドホスト

近日公開

## 🙋 サポート

ご質問、問題提起、新機能のご要望は、[discussion](https://github.com/HanzoAI/Hanzo/discussions)までお気軽にどうぞ

## 🙌 コントリビュート

これらの素晴らしい貢献者に感謝します

<a href="https://github.com/HanzoAI/Hanzo/graphs/contributors">
<img src="https://contrib.rocks/image?repo=HanzoAI/Hanzo" />
</a>

[コントリビューティングガイド](CONTRIBUTING.md)を参照してください。質問や問題があれば、[Discord](https://discord.gg/jbaHfsRVBW) までご連絡ください。
[![Star History Chart](https://api.star-history.com/svg?repos=HanzoAI/Hanzo&type=Timeline)](https://star-history.com/#HanzoAI/Hanzo&Date)

## 📄 ライセンス

このリポジトリのソースコードは、[Apache License Version 2.0](LICENSE.md)の下で利用可能です。
