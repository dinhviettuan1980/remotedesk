# RemoteDesk — ghi chú bàn giao (cập nhật 2026-10-04)

Đọc file này trước, rồi đến [README.md](README.md) (kiến trúc + build/deploy).

## Mục đích
Phần mềm điều khiển máy tính từ xa **cho máy cá nhân của chủ repo**: Mac ở công ty ⇄ Windows ở nhà, xem/điều khiển từ web, app hoặc iPhone. Được viết để thay RustDesk, vì server công cộng của RustDesk bắt đăng nhập.

## Hiện trạng: đang chạy
- **Relay + trang web**: https://remote.tuandv.id.vn, chạy trên VPS `pc1@103.163.216.32`.
  - Code nằm ở `/home/pc1/remote-desk/relay`.
  - Chạy bằng systemd unit `remote-desk`, lắng nghe `127.0.0.1:8790`.
  - nginx site `remote.tuandv.id.vn`, chứng chỉ Let's Encrypt tên `remote-desk`.
  - DNS do PA Việt Nam quản lý, không có wildcard; mỗi subdomain mới phải tự thêm bản ghi A.
- **Mã truy cập**: `REMOTE_TOKEN` trong `relay/.env` trên VPS, bản sao ở `~/remote-desk/.env` local (gitignored). Không commit, không dán vào chat.
- **Các máy đang online**:
  - `macbook-pro-cua-tuandv`, tên hiển thị "MacBook Pro (công ty)": cài **agent + app**.
    - Agent ở `~/Library/Application Support/RemoteDesk/RemoteDesk.app`, chạy bằng LaunchAgent `vn.tuandv.remotedesk`.
    - App điều khiển ở `/Applications/RemoteDesk.app`.
    - Đã cấp quyền Screen Recording và Accessibility cho **RemoteDesk**.
  - `win-guo31ala26r` (WIN-GUO31ALA26R): cài **chỉ agent**.
    - Thư mục `C:\ProgramData\RemoteDesk`, chạy bằng Task Scheduler task `RemoteDesk` khi đăng nhập.
    - Capture bằng gdigrab, nén bằng libx264 vì máy không có encoder GPU dùng được.

## Thành phần trong repo
| Thư mục | Nội dung |
|---|---|
| `agent/` | Go. ffmpeg capture → H.264 (Mac: avfoundation + VideoToolbox; Win: ddagrab/gdigrab + nvenc/qsv/amf/libx264). Điều khiển chuột/phím: CGEvent (Mac), SendInput (Win). Đồng bộ clipboard. `-check` để tự kiểm tra. Test: `go test ./...` (bộ tách frame H.264). |
| `relay/server.js` | Node + ws. Nhiều viewer xem cùng một agent: agent encode 1 lần, relay nhân luồng, giữ sẵn GOP cho người vào sau, mỗi viewer xử lý nghẽn riêng. Đăng nhập bằng token, cookie là HMAC. Phát bộ cài / link tải. |
| `relay/public/` | Viewer web: WebCodecs, chế độ 👁 Chỉ xem / 🖱 Điều khiển (mặc định chỉ xem), zoom (vừa khung / vừa chiều ngang / 100–200%, tự trượt khi chuột sát mép), cảm ứng cho điện thoại + nút ⌨, đổi ⌘⇄Ctrl, menu phím tắt, PWA cài được. |
| `relay/install/` | `install.ps1`, `install.sh`. Server điền `{{SERVER}}`, `{{TOKEN}}`, `{{WITH_APP}}` lúc tải. |
| `setup-win/` | `RemoteDesk-Setup.exe` (Go, manifest xin quyền Admin). Server gắn cấu hình JSON vào cuối file lúc tải (magic `RDCFG1`). |
| `setup-mac/` | Build `.pkg` (không payload, có postinstall) có nhúng token từ `.env`. |
| `viewer-win/` | App điều khiển Windows: Go + WebView2 (`RemoteDesk.exe`). |
| `viewer-mac/` | App điều khiển Mac: Swift + WKWebView (`RemoteDesk-mac.zip`). |
| `scripts/build.sh`, `scripts/deploy.sh` | Build tất cả ra `relay/dist`; deploy bằng rsync + restart (`SSHPASS=… ./scripts/deploy.sh`). |

## Cách deploy nhanh
- Chỉ sửa trang web (`relay/public`): `rsync -az --exclude node_modules/ --exclude .env --exclude dist/ relay/ pc1@103.163.216.32:/home/pc1/remote-desk/relay/`. **Không cần** khởi động lại relay.
- Sửa `server.js`: rsync như trên rồi `sudo systemctl restart remote-desk`. Việc này ngắt mọi phiên đang xem vài giây.
- Sửa agent: `./scripts/build.sh`, rsync `relay/dist`, rồi cài lại agent trên từng máy.
  - **Trên Mac**: binary mới làm macOS bắt cấp lại quyền, vì chữ ký là ad-hoc. Bộ cài so hash (`agent.sha256`) nên chỉ thay khi binary thật sự đổi.
- Bộ cài `.pkg` của Mac phải build lại (`setup-mac/build.sh`) mỗi khi `install.sh` hoặc token đổi. `Setup.exe` của Windows thì không cần, vì server gắn cấu hình lúc tải.

## Chưa kiểm thử trên máy thật
- `RemoteDesk-Setup.exe` / `RemoteDesk-Agent-Setup.exe` (Windows), kể cả bước hỏi Autologon.
- `RemoteDesk-Installer.pkg` / `RemoteDesk-Agent-Installer.pkg` (Mac); phần postinstall chạy dưới user đang đăng nhập.
- App `RemoteDesk.exe` (WebView2): nút toàn màn hình qua Win32.
- App Mac: mới thấy màn hình đăng nhập; chưa thử xem hình và phím trong phiên.
- Cảm ứng / bàn phím trên iPhone, chế độ xem/điều khiển, zoom. Đã deploy nhưng chưa ai xác nhận chạy đúng.

## Vấn đề đã biết / việc nên làm tiếp
1. **Windows thỉnh thoảng rớt kết nối** (WebSocket 1006, khoảng 2 lần / 12 giờ, tự nối lại sau 20–30 giây). Cần đọc `C:\ProgramData\RemoteDesk\agent.log` và Event Log về nguồn/mạng (Modern Standby, card mạng tiết kiệm điện, Wi-Fi). Chủ repo đã có prompt chẩn đoán để chạy trên máy đó.
2. ~~Mac gửi 60 fps dù đặt 30~~: đã thêm filter `fps=N` (agent/session.go), deploy cùng agent 0.2.0.
3. **P2P (WebRTC) đã deploy** (2026-10-04): relay mới + agent 0.2.0 trên cả Mac và Windows. Chạy được khi hai máy cùng mạng (`P2P LAN`). **Mac công ty ⇄ Windows nhà vẫn đi relay**: mạng công ty là NAT đối xứng (STUN thấy cổng công khai khác nhau cho mỗi đích: Google 45206, Cloudflare 29554), nên không đục lỗ UDP được. Muốn cải thiện trường hợp này: TURN (coturn) trên VPS, chỉ đổi TCP → UDP, vẫn đi vòng qua VPS. Chi tiết: `docs/RemoteDesk-TaiLieuKyThuat.pdf`.
4. Chưa có: nhiều màn hình, âm thanh, truyền file. Windows chưa điều khiển được màn hình khoá, UAC hay Ctrl+Alt+Del khi chưa đăng nhập; đang dùng tự đăng nhập (Autologon) để agent chạy sau khi khởi động lại.
5. VPS: RAM khoảng 2 GB, ổ đĩa đã đầy khoảng 91%; relay dùng khoảng 50 MB.

## Bảo mật cần làm
- Mật khẩu SSH/sudo của VPS và một mật khẩu khác đã từng bị dán vào chat; **nên đổi**. Không mật khẩu nào nằm trong code.
- Ai có token là điều khiển được mọi máy đã cài agent; đừng chia sẻ file bộ cài.
- Code **chưa được commit**; nên `git add -A && git commit` để có mốc.
