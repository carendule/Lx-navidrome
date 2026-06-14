<a href="https://github.com/navidrome/navidrome"><img src="resources/logo-192x192.png" alt="Lx-Navidrome logo" title="lx-navidrome" align="right" height="60px" /></a>

# Lx-Navidrome Music Server

> 🎵 **Navidrome × LX Music** — 将本地音乐库管理与多平台在线音源聚合融为一体的自托管音乐服务

[![基于 Navidrome](https://img.shields.io/badge/based%20on-Navidrome-4EACD5?style=flat-square&logo=github)](https://github.com/navidrome/navidrome)
[![LX Music](https://img.shields.io/badge/integrated-LX%20Music-FF6B6B?style=flat-square)](https://github.com/lyswhut/lx-music-desktop)
[![License](https://img.shields.io/github/license/your-username/lx-navidrome?style=flat-square)](LICENSE)
[![Build](https://img.shields.io/github/actions/workflow/status/your-username/lx-navidrome/pipeline.yml?branch=master&logo=github&style=flat-square)](https://github.com/your-username/lx-navidrome/actions)
[![Docker Pulls](https://img.shields.io/docker/pulls/your-dockerhub/lx-navidrome?logo=docker&label=pulls&style=flat-square)](https://hub.docker.com/r/your-dockerhub/lx-navidrome)

Lx-Navidrome 是一个基于 [Navidrome](https://github.com/navidrome/navidrome) 的增强版自托管音乐服务器，深度集成了 [LX Music（洛雪音乐）](https://github.com/lyswhut/lx-music-desktop) 的在线功能。你不仅可以管理和串流本地音乐库，还可以通过 LX Music 自定义音源直接在 Web 界面中搜索、播放来自多个主流音乐平台的在线曲目。

> ⚠️ **声明**：本项目仅供个人学习与技术研究使用，请尊重版权，勿将本项目用于任何商业用途。  
> 🤖 **注意**：项目 90% 以上的代码由 AI 辅助完成，欢迎社区参与改进与审查。

---

## ✨ 核心功能

### 继承自 Navidrome
- 🗂️ 支持**超大本地音乐库**的高效管理与流式播放
- 🎵 支持几乎**所有音频格式**的串流
- 📋 完善的**元数据**读取与展示（专辑封面、歌词、标签等）
- 🎼 出色的**合辑**（Various Artists）与**套装**（多碟专辑）支持
- 👥 **多用户**模式，每位用户拥有独立的播放记录、播放列表与收藏
- ⚡ 极低的**资源占用**
- 🌐 **多平台**支持：macOS、Linux、Windows，提供 **Docker** 镜像
- 📦 覆盖树莓派在内的主流平台的**开箱即用**二进制文件
- 🔄 自动**监控音乐库**变更并更新元数据
- 🎨 基于 [Material UI](https://material-ui.com) 的**可主题化**现代响应式 Web 界面
- 📱 兼容所有 Subsonic / Madsonic / Airsonic [客户端](https://www.navidrome.org/docs/overview/#apps)
- 🔉 按需**转码**，支持 Opus 编码，可按用户/播放器分别设置
- 🌍 支持**多语言**界面

### 新增：LX Music 在线功能集成 🆕
- 🔍 **在线音乐搜索**：直接在 Navidrome Web 界面中搜索多平台在线曲目
- 📦 **自定义音源支持**：兼容 LX Music 生态的自定义 JS 音源脚本，可在线导入
- ⬇️ **一键缓存/下载**：将在线音乐保存至本地库，与本地曲目统一管理
- 📄 **在线歌词同步**：获取并展示在线歌词（含翻译与逐字歌词）
- 🔗 **在线/本地统一界面**：本地库与在线音源在同一界面无缝切换

---

## 🚀 安装

### Docker（推荐）

```yaml
# docker-compose.yml
version: "3"
services:
  lx-navidrome:
    image: your-dockerhub/lx-navidrome:latest
    container_name: lx-navidrome
    restart: unless-stopped
    environment:
      - ND_SCANSCHEDULE=1h
      - ND_LOGLEVEL=info
      - ND_SESSIONTIMEOUT=24h
      - ND_BASEURL=""
    volumes:
      - "./data:/data"
      - "./music:/music:ro"
    ports:
      - "4533:4533"