# RemoteDesk — ghi chú bàn giao (cập nhật 2026-10-19)

Đọc file này trước, rồi đến [README.md](README.md) (kiến trúc + build/deploy). Tài liệu kỹ thuật có sơ đồ: [docs/RemoteDesk-TaiLieuKyThuat.pdf](docs/RemoteDesk-TaiLieuKyThuat.pdf) (viết ngày 2026-10-05, chưa có TURN / nhiều màn hình / âm thanh).

## Mục đích
Phần mềm điều khiển máy tính từ xa **cho máy cá nhân của chủ repo**: Mac ở công ty ⇄ Windows ở nhà, xem/điều khiển từ web, app (Windows / Mac / iPhone). Viết để thay RustDesk, vì server công cộng của RustDesk bắt đăng nhập.

## Hiện trạng: đang chạy
- **Relay + trang web**: https://remote.tuandv.id.vn, chạy trên VPS `pc1@103.163.216.32`.
  - Code ở `/home/pc1/remote-desk/relay`, systemd unit `remote-desk`, lắng nghe `127.0.0.1:8790`.
  - nginx site `remote.tuandv.id.vn`, chứng chỉ Let's Encrypt tên `remote-desk`.
  - **TURN**: coturn trên cùng VPS (3478 UDP/TCP, 5349 TLS), cài bằng `scripts/setup-turn.sh`. Relay ký thông tin đăng nhập tạm (24 h) từ `TURN_SECRET` trong `relay/.env`: viewer nhận trong `hello`, agent nhận `{"t":"ice"}` (agent 0.2.6+ mới dùng; agent cũ chỉ có STUN).
  - DNS do PA Việt Nam quản lý, không có wildcard; mỗi subdomain mới phải tự thêm bản ghi A.
- **Mã truy cập**: nhiều người dùng, mỗi token chỉ thấy máy của mình. Token admin = `REMOTE_TOKEN` trong `relay/.env` trên VPS, bản sao ở `~/remote-desk/.env` local (gitignored). Admin có thể đặt mật khẩu riêng ở `#/admin`. Người dùng khác lưu ở `/home/pc1/remote-desk/data/users.json` (ngoài thư mục rsync). Không commit, không dán token vào chat.
- **Phiên bản**: agent **0.3.0** (2026-10-19): chất lượng tự động + GOP ngắn khi P2P mất hình, clipboard ảnh, khởi động lại máy từ xa, thông báo khi có người xem (bật/tắt từ viewer), cập nhật từ danh sách máy. 0.2.7: PLI không còn restart encoder. 0.2.6: nhiều màn hình, âm thanh, TURN. Agent Mac cần macOS 13+ (build đặt `MACOSX_DEPLOYMENT_TARGET=13.0`).
- **Các máy**:
  - `macbook-pro-cua-tuandv` "MacBook Pro (công ty)": **agent + app**, macOS 26, một màn hình.
    - Agent: `~/Library/Application Support/RemoteDesk/RemoteDesk.app` (LaunchAgent `vn.tuandv.remotedesk`), ký bằng chứng chỉ Apple Development cố định nên cập nhật không mất quyền Screen Recording / Accessibility.
    - App điều khiển: `/Applications/RemoteDesk.app`.
  - `win-guo31ala26r` (WIN-GUO31ALA26R): **chỉ agent**, `C:\ProgramData\RemoteDesk`, Task Scheduler task `RemoteDesk` khi đăng nhập. Capture gdigrab + libx264 (không có encoder GPU dùng được).
  - iPhone: app `viewer-ios/` cài qua cáp (`viewer-ios/build.sh`).

## Thành phần trong repo
| Thư mục | Nội dung |
|---|---|
| `agent/` | Go. ffmpeg capture → H.264 (Mac: avfoundation + VideoToolbox; Win: ddagrab/gdigrab + nvenc/qsv/amf/libx264). Điều khiển: CGEvent (Mac), SendInput (Win). Clipboard, chuyển file, P2P (pion). Nhiều màn hình: `display*.go`. Âm thanh: `audio.go` + `audio_darwin.m` (ScreenCaptureKit) + `audio_windows.go` (WASAPI loopback). `-check` để tự kiểm tra. Test: `go test ./...`. |
| `relay/server.js` | Node + ws. Nhiều viewer xem cùng một agent: agent nén một lần, relay nhân luồng, giữ sẵn GOP cho người vào sau, mỗi viewer xử lý nghẽn riêng. Định tuyến âm thanh (nhị phân loại 3) chỉ tới viewer đang bật 🔊 và không đi P2P. Đăng nhập bằng token, cookie là HMAC. Phát bộ cài, cấp thông tin TURN. |
| `relay/public/` | Viewer web: WebCodecs, 👁/🖱, zoom, 🖥 chọn màn hình, 🔇/🔊, 📁 chuyển file, cảm ứng (touchpad / chạm trực tiếp, ⌨ + hàng phím), đổi ⌘⇄Ctrl, menu phím tắt, PWA. |
| `relay/install/` | `install.ps1`, `install.sh`. Server điền `{{SERVER}}`, `{{TOKEN}}`, `{{WITH_APP}}` lúc tải. |
| `setup-win/` | Bộ cài NSIS (`nsis/installer.nsi` + `register.ps1`), đóng gói agent + ffmpeg gyan essentials + app. Relay gắn cấu hình JSON vào cuối file lúc tải (magic `RDCFG1`): link thường chỉ có `server` → trang "Mã truy cập" hỏi mã và kiểm tra bằng `remote-agent -check-token`; link `?t=` / cập nhật từ xa có sẵn `token` → bỏ qua trang đó. |
| `setup-mac/` | `.pkg` không chứa token: postinstall lấy token từ link tải (`?t=`) nếu có, không thì hỏi (để trống = giữ mã cũ). |
| `viewer-win/` | App Windows: Go + WebView2. ⛶ toàn màn hình (cửa sổ không viền phủ cả taskbar); khi đang 🖱 điều khiển thì hook bàn phím cấp thấp (`keyhook.go`) gửi Win / Alt+Tab / Alt+Esc / Ctrl+Esc / Alt+Space sang máy kia. Ctrl+Alt+Enter để thoát. |
| `viewer-mac/` | App Mac: Swift + WKWebView (`RemoteDesk-mac.zip`). ⌘W/⌘Q/⌘T gửi sang máy kia. |
| `viewer-ios/` | App iPhone/iPad: UIKit + WKWebView toàn màn hình, tự mở bàn phím khi click trúng ô nhập ở máy kia, giữ màn hình sáng, file tải về mở share sheet. XcodeGen (`project.yml`); `build.sh` build và cài qua cáp; `--dev URL` để thử relay khác. |
| `scripts/` | `build.sh` (test + build tất cả ra `relay/dist`), `deploy.sh` (build + rsync + restart), `setup-turn.sh`, `setup-nsis.sh`. |

## Giao thức (tóm tắt, chi tiết ở đầu `relay/server.js`)
- Nhị phân agent → viewer: `[loại][cờ][ts u32][dữ liệu]`. 1 = H.264, 2 = file, 3 = âm thanh Opus (20 ms/gói).
- `info` (agent → viewer, relay giữ bản mới nhất): `os`, `enc`, `capture`, `screens` [{name, w, h, primary}], `screen`, `audio`. Agent 0.2.6 gửi `info` ngay khi bắt đầu chụp (chưa có `enc`), và gửi lại khi đã ra hình.
- `{"t":"screen","i":N}` viewer → agent: đổi màn hình (agent khởi động lại ffmpeg, áp dụng cho mọi người đang xem).
- `{"t":"audio","on":bool}` viewer → relay; relay → agent `{"t":"audio","vids":[…],"ws":bool}`.
- P2P: viewer chỉ thêm transceiver audio khi agent ≥ 0.2.6; agent chỉ thêm track Opus khi offer có `m=audio`. Nhờ vậy viewer cũ ⇄ agent mới và ngược lại vẫn chạy.

## Cách deploy nhanh
- Đầy đủ: `SSHPASS=… ./scripts/deploy.sh` (cần mật khẩu sudo VPS).
- Chỉ trang web (`relay/public`): `rsync -az --exclude node_modules/ --exclude .env --exclude dist/ relay/ pc1@103.163.216.32:/home/pc1/remote-desk/relay/`. **Không cần** khởi động lại relay.
- Sửa `server.js`: rsync như trên rồi `sudo systemctl restart remote-desk`. Mọi phiên đang xem bị ngắt vài giây.
- Sửa agent: `./scripts/build.sh`, rsync `relay/dist`, rồi cập nhật từng máy ("Phím…" → "Cập nhật agent").
- Bộ cài `.pkg` của Mac phải build lại mỗi khi `install.sh` đổi (build.sh làm việc này).

## Đã thử / chưa thử
- Agent 0.2.6 trên Mac này (relay local, viewer thật trong Chrome headless):
  - Âm thanh qua relay và qua track WebRTC: chạy được (ScreenCaptureKit → Opus).
  - Ô 🖥: hiện khi có 2 màn hình, gửi `screen`, nhớ lựa chọn theo tên màn hình.
  - Tương thích: viewer mới + agent 0.2.5, offer P2P không audio + agent mới.
- Chưa thử được:
  - **Video trên Mac khi agent chạy từ phiên dev**: avfoundation không ra hình, kể cả với binary 0.2.5 không sửa gì (quyền Screen Recording chỉ áp dụng cho agent chạy bằng LaunchAgent thật). Agent thật vẫn stream bình thường.
  - Nhiều màn hình trên Windows; chuột / con trỏ chữ khi xem màn hình thứ hai trên Mac (chỉ thử phần chụp).
  - Clipboard ảnh trên Windows, ảnh từ máy kia về trình duyệt, khởi động lại máy, thông báo trên Windows.
  - Toàn bộ phần Windows của 0.2.6: liệt kê màn hình (EnumDisplayMonitors + QueryDisplayConfig + DXGI), ddagrab `output_idx` / gdigrab theo offset, chuột `MOUSEEVENTF_VIRTUALDESK`, WASAPI loopback, P2P qua TURN. Đã build chéo và vet; prompt kiểm tra trên máy Windows nằm trong lịch sử chat ngày 2026-10-15.
  - Phím Win / Alt+Tab của app Windows ở chế độ toàn màn hình (commit 8d63b49).
  - Bộ cài NSIS (cài đè, gỡ, cập nhật từ xa), `.pkg` của Mac, app iPhone trên máy thật ngoài lần cài đầu.

## Vấn đề đã biết / việc nên làm tiếp
1. **Windows thỉnh thoảng rớt kết nối** (WebSocket 1006, khoảng 2 lần / 12 giờ, tự nối lại sau 20–30 giây). Từ 2026-10-12 agent giữ màn hình sáng khi cắm điện; cần theo dõi xem còn rớt không.
2. **TURN qua mạng công ty (2026-10-16)**: P2P qua TURN chạy được (`p2p[N]: via … relay 103.163.216.32`), nhưng mạng công ty mất gói. Agent 0.2.6 khởi động lại encoder mỗi lần có PLI (89 lần trong một buổi sáng), mỗi lần lại bắn một keyframe lớn qua UDP; cùng lúc đó WebSocket của agent và các kết nối TCP tới VPS bị reset ở cả hai đầu (nghi firewall công ty). Agent 0.2.7 bỏ việc khởi động lại encoder khi có PLI (chờ keyframe tự nhiên ≤ 3 s). Nếu vẫn rớt: thử bỏ ô P2P (đi relay TCP 443) để so sánh.
3. **Mac công ty ⇄ Windows nhà**: mạng công ty là NAT đối xứng, nên P2P trực tiếp không được. Viewer đã nhận thông tin TURN từ 2026-10-15, agent nhận từ 0.2.6; kỳ vọng thanh trạng thái hiện `P2P TURN` (UDP vòng qua VPS). Chưa xác nhận.
4. Binary agent Mac: trước 0.3.0 build ra `minos 26.0` (theo máy build); từ 0.3.0 `scripts/build.sh` đặt 13.0 (mức thấp nhất Go 1.27 cho phép).
5. Chưa có: micro (âm thanh viewer → máy kia), xem nhiều màn hình cùng lúc, điều khiển màn hình khoá / UAC / Ctrl+Alt+Del trên Windows (agent chạy trong phiên người dùng; đang dùng Autologon để agent chạy sau khi khởi động lại).
6. VPS: RAM khoảng 2 GB; ổ đĩa 68% sau khi dọn journal ngày 2026-10-19 (giới hạn journald 300 MB: `/etc/systemd/journald.conf.d/size.conf`). `/home/pc1` chiếm 6 GB. Lịch sử truy cập: `/home/pc1/remote-desk/data/audit.jsonl` (xoay vòng ở 5 MB).

## Bảo mật cần làm
- Mật khẩu SSH/sudo của VPS và một mật khẩu khác đã từng bị dán vào chat; **nên đổi**. Không mật khẩu nào nằm trong code.
- Ai có một token là điều khiển được mọi máy cài bằng token đó. Bộ cài chung (`/dl/RemoteDesk-*Setup.exe`, `/dl/RemoteDesk-*Installer.pkg`) không chứa token và ai cũng tải được (từ 2026-10-19); link có `?t=` và lệnh dòng lệnh thì có token.
