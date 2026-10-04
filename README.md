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

**P2P (WebRTC)**: khi mạng cho phép, video (RTP) và input (data channel) đi thẳng agent ⇄ viewer qua UDP; relay chỉ làm signaling. Phiên luôn bắt đầu qua relay và tự quay về relay nếu P2P không được. Thanh trạng thái hiện `relay` / `P2P` / `P2P LAN`. Tài liệu kỹ thuật đầy đủ: [docs/RemoteDesk-TaiLieuKyThuat.pdf](docs/RemoteDesk-TaiLieuKyThuat.pdf).

- **Agent** ([agent/](agent/)) tự kết nối ra ngoài, nên không cần mở port hay NAT.
  - Chỉ chạy ffmpeg khi có viewer kết nối.
  - macOS: capture bằng `avfoundation`, nén bằng `h264_videotoolbox`, điều khiển bằng CGEvent.
  - Windows: capture bằng `ddagrab` (dự phòng `gdigrab`), nén bằng nvenc / qsv / amf (dự phòng libx264), điều khiển bằng SendInput.
- **Relay** ([relay/server.js](relay/server.js)) không giải mã video, tốn khoảng 50 MB RAM. Khi viewer nhận chậm, relay bỏ frame cho tới keyframe kế tiếp.
- **Viewer** ([relay/public/](relay/public/)) là trang web, máy điều khiển không cần cài gì.
  - Phím được gửi theo vị trí vật lý (`KeyboardEvent.code`), nên Telex/Unikey ở máy kia gõ tiếng Việt bình thường.
  - Có tuỳ chọn đổi ⌘ ⇄ Ctrl khi điều khiển khác hệ điều hành.
  - Clipboard đồng bộ hai chiều.
  - Chuyển file hai chiều: copy file rồi ⌘V/Ctrl+V vào màn hình máy kia; kéo thả; nút 📁 để duyệt và tải file của máy kia (agent 0.2.2+).

## Bảo mật

Mỗi token là một người dùng riêng: token chỉ thấy và điều khiển các máy được cài bằng chính token đó.

- **Admin**: `REMOTE_TOKEN` trong `relay/.env` trên VPS (bản sao ở `.env` local, không commit). Admin tạo / đổi mã / khoá / xoá người dùng ở trang `#/admin`, nhưng không thấy máy của họ.
- **Người dùng**: lưu ở `data/users.json` trên VPS (ngoài thư mục deploy, quyền 0600). Bấm "Copy tin nhắn gửi họ" để gửi link cài + mã.
- Agent gửi token trong header.
- Trình duyệt nhập token một lần, sau đó giữ cookie HttpOnly (id người dùng + HMAC của token, không phải token). Đổi mã hoặc khoá thì cookie, máy và phiên đang xem của người đó bị ngắt ngay.
- Bộ cài `.pkg` của Mac không chứa token: nó đọc token từ link tải mà macOS lưu kèm file, nếu không có thì hỏi.
- Đăng nhập sai 10 lần thì IP bị khoá 15 phút. nginx cũng giới hạn tốc độ `/login`.
- WebSocket của viewer kiểm tra Origin.

Lộ token nghĩa là người khác điều khiển được các máy của token đó. Token người dùng: bấm "Đổi mã" trên trang admin rồi cài lại máy. Token admin: sửa `.env` trên VPS, restart relay, rồi cài lại các máy của admin.

## Cài đặt

Mở `https://remote.tuandv.id.vn` → đăng nhập → **Cài RemoteDesk lên máy tính**. Mỗi hệ điều hành có 2 lựa chọn:

| | Chỉ agent | Agent + App |
|---|---|---|
| Máy được điều khiển | ✓ | ✓ |
| App RemoteDesk để điều khiển máy khác | — (dùng trình duyệt / iPhone) | ✓ Desktop + Start Menu (Win) / Applications (Mac) |
| Windows | `RemoteDesk-Agent-Setup.cmd` | `RemoteDesk-Setup.cmd` |
| macOS | `curl … install.sh?t=…&app=0 \| bash` | `curl … install.sh?t=… \| bash` |

- Windows: agent + ffmpeg vào `C:\ProgramData\RemoteDesk`, chạy bằng Task Scheduler khi đăng nhập (quyền cao nhất), tắt sleep khi cắm điện.
- macOS: agent là `~/Library/Application Support/RemoteDesk/RemoteDesk.app` (LaunchAgent); cần cấp **Screen Recording** + **Accessibility** cho RemoteDesk. Bộ cài chỉ thay agent khi có bản mới (so hash), nên chạy lại không làm mất quyền.
- App điều khiển: `viewer-win/` (Go + WebView2) và `viewer-mac/` (Swift + WKWebView) — cửa sổ bọc trang viewer; trên Mac ⌘W/⌘Q/⌘T được gửi sang máy kia.
- Điện thoại: dùng trình duyệt (chạm = click, giữ = click phải, 2 ngón = cuộn, nút ⌨ = bàn phím).

`remote-agent -check` để tự kiểm tra trên máy; log ở `agent.log` cạnh `config.json`.

## Phát triển & deploy

```bash
./scripts/build.sh                 # test + build agent (Windows exe, macOS universal) → relay/dist
SSHPASS=… ./scripts/deploy.sh      # build + rsync relay + restart systemd
SSHPASS=… ./scripts/deploy.sh --setup   # lần đầu: token, nginx, certbot
```

## Giới hạn hiện tại (v0.1)

- Chỉ điều khiển màn hình chính.
- Windows: chưa điều khiển được màn hình khoá, UAC, Ctrl+Alt+Del (cần Windows Service chạy SYSTEM). Nếu máy khởi động lại, agent chỉ chạy sau khi đăng nhập, nên bật tự đăng nhập.
- Chưa có âm thanh và truyền file.
- Trên Mac, trình duyệt giữ lại một số phím tắt (⌘Tab, ⌘Q). Bấm ⛶ để vào toàn màn hình, Chrome/Edge sẽ khoá phím hệ thống, hoặc dùng menu "Phím…".
