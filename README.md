# Resend Server

プライバシー保護を目的に、各種Webサービス登録用のメールアドレス（エイリアス）を管理し、Resend経由でメールを送信する個人用Webサーバーです。

Cloudflare Email Routing（受信）とResend（送信）を組み合わせ、SQLiteによる履歴保管とGoogle Driveへの暗号化バックアップ（AES-256-GCM）を備えています。TailscaleとRaspberry Piでの常時起動を想定しています。

## 主な機能

- **メール作成・送信**: 登録済みエイリアスまたは直接入力による送信。Resend API連携。
- **エイリアス管理**: サービスごとの登録用アドレス、表示名、メモの管理。
- **送信履歴**: 送信日時、宛先、差出人、件名、本文、送信結果の保存・閲覧。
- **Google Drive暗号化退避**: SQLiteデータベースをAES-256-GCMで暗号化してGoogle Driveへ自動・手動アップロード。
- **簡易ログイン認証**: Tailscale内の家族端末等からの誤送信を防ぐID・パスワード保護。
- **Material Design 3 UI**: HTMXとVanilla CSSによるモダンで軽快な操作画面。テーマ切り替えに対応。

## 開発・ローカル実行手順 (Windows / WSL2 / Linux)

### 1. 設定ファイルの作成
```bash
cp .env.example .env
```
`.env` ファイルを開き、必要な項目（`RESEND_API_KEY`, `DEFAULT_DOMAIN`, `ADMIN_PASSWORD` 等）を設定します。

### 2. ローカル起動
```bash
go run ./cmd/server
```
ブラウザで `http://localhost:8080` を開きます。

## Docker による実行手順 (Raspberry Pi / サーバー環境)

```bash
docker compose up -d --build
```
ログ確認:
```bash
docker compose logs -f
```

## Google Drive 退避のセットアップ

1. Google Cloud Console でプロジェクトを作成し、Google Drive API を有効化します。
2. サービスアカウントを作成し、JSON鍵ファイルをダウンロードします。
3. ダウンロードしたJSONファイルを `./data/gdrive-service-account.json` に配置します。
4. Google Drive上でバックアップ保存用のフォルダを作成し、そのフォルダにサービスアカウントのメールアドレスに対する「編集者」権限を共有します。
5. フォルダのURLからフォルダID（URL末尾の英数字）をコピーし、`.env` の `GDRIVE_FOLDER_ID` に指定します。
