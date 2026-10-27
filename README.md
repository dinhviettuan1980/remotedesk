# RemoteDesk

Điều khiển máy tính từ xa cho cá nhân: Mac công ty ⇄ Windows ở nhà, qua relay trên VPS. Không hỏi mật khẩu mỗi lần kết nối. Mã truy cập chỉ nhập một lần, trình duyệt nhớ 1 năm.

```
 Máy bị điều khiển                       VPS 103.163.216.32                      Máy điều khiển
┌──────────────────────┐   wss (443)   ┌──────────────────────────┐   wss (443)   ┌───────────────────┐
│ remote-agent (Go)    │──────────────►│ nginx + TLS              │◄──────────────│ Chrome / Edge     │
│  ffmpeg: chụp màn    │  H.264 frames │ relay (Node, :8790)      │  JSON input   │  WebCodecs decode │
│  hình → H.264 (HW)   │◄──────────────│  ghép 1 agent ↔ 1 viewer │──────────────►│  canvas + phím/   │
│  SendInput / CGEvent │  phím, chuột  │  chỉ chuyển tiếp byte    │   video       │  chuột            │
└──────────────────────┘               └──────────────────────────┘               └───────────────────┘
```

**P2P (WebRTC)**: khi mạng cho phép, video (RTP), âm thanh (Opus) và input (data channel) đi thẳng agent ⇄ viewer qua UDP; relay chỉ làm signaling. Khi hai đầu không đục lỗ được (NAT đối xứng ở công ty), media đi qua TURN (coturn trên VPS, UDP 3478). Phiên luôn bắt đầu qua relay và tự quay về relay nếu P2P không được. Thanh trạng thái hiện `relay` / `P2P` / `P2P LAN` / `P2P TURN`. Tài liệu kỹ thuật đầy đủ: [docs/RemoteDesk-TaiLieuKyThuat.pdf](docs/RemoteDesk-TaiLieuKyThuat.pdf).

- **Agent** ([agent/](agent/)) tự kết nối ra ngoài, nên không cần mở port hay NAT.
  - Chỉ chạy ffmpeg khi có viewer kết nối.
  - macOS: capture bằng `avfoundation`, nén bằng `h264_videotoolbox`, điều khiển bằng CGEvent. Âm thanh hệ thống: ScreenCaptureKit (macOS 13+, dùng quyền Screen Recording).
  - Windows: capture bằng `ddagrab` (dự phòng `gdigrab`), nén bằng nvenc / qsv / amf (dự phòng libx264), điều khiển bằng SendInput. Âm thanh hệ thống: WASAPI loopback trên thiết bị phát mặc định.
  - Âm thanh: agent thu PCM rồi đưa ffmpeg dạng WAV qua stdin, ffmpeg nén `libopus` (có trong ffmpeg Homebrew và bản gyan.dev essentials).
- **Relay** ([relay/server.js](relay/server.js)) không giải mã video, tốn khoảng 50 MB RAM. Khi viewer nhận chậm, relay bỏ frame cho tới keyframe kế tiếp.
- **Viewer** ([relay/public/](relay/public/)) là trang web, máy điều khiển không cần cài gì.
  - Phím được gửi theo vị trí vật lý (`KeyboardEvent.code`).
  - **Gõ tiếng Việt** bằng bộ gõ của **máy điều khiển** (agent 0.3.1+): bộ gõ macOS (Telex/VNI), Windows IME, Unikey/EVKey/GoTiengViet. Viewer giữ focus ở một ô nhập ẩn để bộ gõ ghép chữ, rồi gửi chữ đã ghép (`{"t":"txt"}`); agent gõ chữ đó bằng `SendInput` + `KEYEVENTF_UNICODE` (Windows) / `CGEventKeyboardSetUnicodeString` (Mac), nên vào được cả ô nhập của app Electron/WebView. Khi đó **tắt bộ gõ ở máy bị điều khiển**. Từ đang ghép (bộ gõ macOS chỉ chốt khi hết từ) hiện ở vị trí con trỏ chữ. Agent cũ hơn 0.3.1: như trước, bộ gõ ở máy kia gõ.
  - "Phím…" → **Gõ nội dung clipboard**: gõ từng ký tự thay vì dán, cho màn hình đăng nhập, console máy ảo, ô không cho dán (agent 0.3.1+).
  - Có tuỳ chọn đổi ⌘ ⇄ Ctrl khi điều khiển khác hệ điều hành.
  - Clipboard đồng bộ hai chiều.
  - Chuyển file hai chiều: copy file rồi ⌘V/Ctrl+V vào màn hình máy kia; kéo thả; nút 📁 để duyệt và tải file của máy kia (agent 0.2.2+).
  - **Nhiều màn hình** (agent 0.2.6+): ô 🖥 trên thanh công cụ (chỉ hiện khi máy kia có từ 2 màn hình) chọn màn hình để xem. Chuột, bàn phím và vị trí con trỏ chữ theo màn hình đang xem. Trình duyệt nhớ lựa chọn theo từng máy, giống zoom.
  - **Chất lượng tự động** (mặc định): viewer đo mất gói / số frame nhận được, tự hạ xuống 720p khi mạng xấu và nâng lại sau một phút ổn định. Đã ở 720p mà đường TURN vẫn mất gói thì bỏ TURN trong phiên đó (P2P trực tiếp nếu được, không thì relay). Agent 0.3.0 tự chuyển sang keyframe mỗi giây khi P2P liên tục mất hình.
  - **Clipboard ảnh** (agent 0.3.0+): chụp màn hình rồi ⌘V/Ctrl+V vào máy kia là dán ảnh; ảnh copy ở máy kia vào thẳng clipboard máy này.
  - Menu "Phím…" (agent 0.3.0+): **Khởi động lại máy kia**; bật/tắt **thông báo trên máy kia khi có người xem** (mặc định tắt, lưu trong `config.json` của máy đó).
  - **Âm thanh** (agent 0.2.6+): nút 🔇/🔊, mặc định tắt; agent chỉ thu tiếng khi có viewer đang bật. Opus 96 kbps, đi qua relay (WebSocket) hoặc track WebRTC khi P2P. Ngoài thời gian truyền: nén khoảng 20 ms, viewer đệm khoảng 60 ms. Cần WebCodecs `AudioDecoder` giải được Opus (Chrome/Edge, app Windows); trình duyệt không có thì nút bị ẩn.

## Bảo mật

Mỗi token (mã truy cập) là một nhóm máy: admin tạo mã, máy cài bằng mã nào thì vào nhóm đó, và ai đăng nhập bằng mã đó thì thấy và điều khiển mọi máy trong nhóm. Trong một nhóm mọi người dùng chung mã, không phân biệt quản lý / thành viên.

- **Admin**: `REMOTE_TOKEN` trong `relay/.env` trên VPS (bản sao ở `.env` local, không commit). Admin tạo / đổi mã / khoá / xoá người dùng ở trang `#/admin`, nhưng không thấy máy của họ.
- **Người dùng**: lưu ở `data/users.json` trên VPS (ngoài thư mục deploy, quyền 0600). Bấm "Copy tin nhắn gửi họ" để gửi mã + link bộ cài chung.
- Agent gửi token trong header.
- Trình duyệt nhập token một lần, sau đó giữ cookie HttpOnly (id người dùng + HMAC của token, không phải token). Đổi mã hoặc khoá thì cookie, máy và phiên đang xem của người đó bị ngắt ngay.
- **Bộ cài dùng chung, không chứa mã**: ai cũng tải được (kể cả từ trang đăng nhập). Khi cài, bộ cài hỏi mã truy cập và kiểm tra với relay (`/api/token-check`); máy đã cài thì để trống để giữ nhóm cũ, nhập mã khác để chuyển nhóm. Link có `?t=` (và cập nhật agent từ xa) vẫn mang sẵn mã và cài không cần hỏi.
- Đăng nhập sai 10 lần thì IP bị khoá 15 phút. nginx cũng giới hạn tốc độ `/login`.
- WebSocket của viewer kiểm tra Origin.
- **Hỏi trước khi cho xem** (agent 0.3.3+, tuỳ chọn từng máy): thêm `"confirm": true` vào `config.json` của máy đó rồi khởi động lại agent. Mỗi phiên, máy đó hiện hộp thoại "… muốn xem và điều khiển máy này. Cho phép?"; 30 giây không trả lời là từ chối. Chưa được phép thì agent không chụp màn hình, không nhận phím/chuột, không gửi clipboard/file/âm thanh; từ chối thì viewer thoát ra. Chỉ sửa được tại máy (viewer không tắt được). Người vào sau trong một phiên đã được phép chỉ được báo, không hỏi lại. Đừng bật trên máy không có người ngồi.
- **Cảnh báo máy offline** (tuỳ chọn): đặt `ALERT_TELEGRAM_TOKEN` (bot) + `ALERT_TELEGRAM_CHAT` trong `relay/.env` rồi restart relay. Máy mất kết nối quá `ALERT_AFTER_MIN` phút (mặc định 5) thì nhắn một tin, có lại thì nhắn thêm một tin. Chỉ máy thuộc `ALERT_TENANTS` (mặc định `admin`).

Lộ token nghĩa là người khác điều khiển được các máy của token đó. Token người dùng: bấm "Đổi mã" trên trang admin rồi cài lại máy. Token admin: sửa `.env` trên VPS, restart relay, rồi cài lại các máy của admin.

## Cài đặt

Mở `https://remote.tuandv.id.vn`, tải bộ cài (trang đăng nhập, hoặc sau khi đăng nhập: **Cài RemoteDesk lên máy tính**), chạy, nhập **mã truy cập** khi được hỏi. Mỗi hệ điều hành có 2 lựa chọn:

| | Chỉ agent | Agent + App |
|---|---|---|
| Máy được điều khiển | ✓ | ✓ |
| App RemoteDesk để điều khiển máy khác | — (dùng trình duyệt / iPhone) | ✓ Desktop + Start Menu (Win) / Applications (Mac) |
| Windows | `/dl/RemoteDesk-Agent-Setup.exe` | `/dl/RemoteDesk-Setup.exe` |
| macOS | `/dl/RemoteDesk-Agent-Installer.pkg` | `/dl/RemoteDesk-Installer.pkg` |
| Dòng lệnh (mang sẵn mã) | `curl … install.sh?t=…&app=0 \| bash` / `RemoteDesk-Agent-Setup.cmd` | `curl … install.sh?t=… \| bash` / `RemoteDesk-Setup.cmd` |

- Windows: agent + ffmpeg vào `C:\ProgramData\RemoteDesk`, chạy bằng Task Scheduler khi đăng nhập (quyền cao nhất), tắt sleep khi cắm điện.
- macOS: agent là `~/Library/Application Support/RemoteDesk/RemoteDesk.app` (LaunchAgent); cần cấp **Screen Recording** + **Accessibility** cho RemoteDesk. Bộ cài chỉ thay agent khi có bản mới (so hash), nên chạy lại không làm mất quyền.
- App điều khiển: `viewer-win/` (Go + WebView2) và `viewer-mac/` (Swift + WKWebView) — cửa sổ bọc trang viewer; trên Mac ⌘W/⌘Q/⌘T được gửi sang máy kia.
  - App Windows, bấm ⛶ khi đang 🖱 Điều khiển: cửa sổ phủ cả màn hình (ẩn taskbar, thanh công cụ thu lên mép trên). Phím Windows, Alt+Tab, Alt+Esc, Ctrl+Esc, Alt+Space gửi sang máy kia. **Ctrl+Alt+Enter** để thoát. Không chặn được Ctrl+Alt+Del và Win+L.
- Điện thoại: dùng trình duyệt. Mặc định màn hình là **touchpad** (kéo = di chuột, chạm = click, chạm 2 ngón / giữ = click phải, giữ rồi kéo = kéo thả, 2 ngón = cuộn, chụm 2 ngón = zoom); đổi sang "chạm trực tiếp" ở ⋯ → ☝. Nút ⌨ mở bàn phím kèm hàng phím Esc / Tab / Ctrl / Alt / ⌘ / ⇧ / mũi tên (Ctrl/Alt/⌘/⇧ dính cho phím kế tiếp, chạm đúp để khoá). Xoay ngang thì thanh công cụ tự ẩn, chạm ▾ ở mép trên để hiện.

`remote-agent -check` để tự kiểm tra trên máy; log ở `agent.log` cạnh `config.json`.

**Cập nhật agent**: trang danh sách máy hiện "có bản mới" và nút **Cập nhật** cạnh máy chạy bản cũ (Windows 0.2.1+, Mac 0.2.3+), không cần vào phiên. Trong phiên: "Phím…" → "Cập nhật agent". Từ 0.3.3 agent **tự cập nhật**: 10 phút sau khi chạy rồi cứ 6 giờ một lần, nếu `dist/agent-version.txt` trên relay mới hơn và không có ai đang xem thì tự cài. Mỗi bản chỉ thử một lần (`autoUpdateTried` trong `config.json`); `"autoUpdate": false` để tắt. **Lịch sử truy cập**: mục cuối trang danh sách máy (ai xem máy nào, lúc nào, bao lâu, từ thiết bị / IP nào).

## Phát triển & deploy

```bash
./scripts/build.sh                 # test + build agent (Windows exe, macOS universal) → relay/dist
SSHPASS=… ./scripts/deploy.sh      # build + rsync relay + restart systemd
SSHPASS=… ./scripts/deploy.sh --setup   # lần đầu: token, nginx, certbot
```

**GitHub Actions** ([.github/workflows/build.yml](.github/workflows/build.yml)): mỗi lần push lên `main`, test trên Windows và chạy `scripts/build.sh` trên runner macOS (artifact `dist-<version>`, giữ 14 ngày). Push tag `v<version>` (khớp `version` trong `agent/main.go`) thì deploy thêm `relay/public` + `relay/dist` lên VPS (không restart; sửa `server.js` vẫn dùng `deploy.sh`). Deploy cần secrets `MAC_CERT_P12` + `MAC_CERT_PASSWORD` (chứng chỉ Apple Development, base64) và `DEPLOY_SSH_KEY`; thiếu chứng chỉ thì **không deploy**, vì agent Mac ký ad-hoc sẽ mất quyền Screen Recording / Accessibility khi các máy tự cập nhật.

## Giới hạn hiện tại

- Xem một màn hình mỗi lần; mọi người đang xem cùng một máy thấy cùng màn hình (agent chỉ nén một luồng).
- Windows: chưa điều khiển được màn hình khoá, UAC, Ctrl+Alt+Del (agent chạy trong phiên người dùng; Mac thì mở khoá được vì màn hình khoá vẫn thuộc phiên đó). Nếu máy khởi động lại, agent chỉ chạy sau khi đăng nhập, nên bật tự đăng nhập. Màn hình nối vào card đồ hoạ thứ hai không chụp được bằng `ddagrab`; agent dùng `gdigrab` cho màn hình đó.
- Âm thanh chỉ một chiều (máy bị điều khiển → viewer), chưa có micro. Mac cần macOS 13 trở lên. Trình duyệt không giải được Opus bằng `AudioDecoder` (một số bản Safari / iPhone) thì không có nút 🔊.
- Trên Mac, trình duyệt giữ lại một số phím tắt (⌘Tab, ⌘Q). Bấm ⛶ để vào toàn màn hình, Chrome/Edge sẽ khoá phím hệ thống, hoặc dùng menu "Phím…".
