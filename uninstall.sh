#!/bin/bash

# Colors
GREEN="\033[1;32m"
YELLOW="\033[1;33m"
CYAN="\033[1;36m"
RED="\033[1;31m"
RESET="\033[0m"
BOLD="\033[1m"
GRAY="\033[1;30m"

print_task() { echo -ne "${GRAY}•${RESET} $1..."; }
print_done() { echo -e "\r${GREEN}✓${RESET} $1      "; }
print_warning() { echo -e "\r${YELLOW}⚠${RESET} $1      "; }

run_silent() {
  local msg="$1"
  local cmd="$2"
  print_task "$msg"
  bash -c "$cmd" &>/tmp/zivpn_uninstall.log
  [ $? -eq 0 ] && print_done "$msg" || print_warning "$msg"
}

clear
echo -e "${BOLD}ZiVPN UDP Uninstaller${RESET}"
echo -e "${GRAY}GeekzBot Edition${RESET}"
echo ""

echo -e "${YELLOW}⚠️  WARNING: This will remove all ZiVPN components!${RESET}"
read -p "Are you sure? (y/N): " confirm

[[ "$confirm" != "y" && "$confirm" != "Y" ]] && {
  echo -e "${RED}Uninstallation cancelled.${RESET}"
  exit 0
}

echo ""
echo -e "${BOLD}Starting Uninstallation...${RESET}"
echo ""

# ========= STOP & DISABLE SERVICES =========
run_silent "Stopping services" \
"systemctl stop zivpn.service zivpn-api.service zivpn-bot.service zivpn-backup.service 2>/dev/null"

run_silent "Disabling services" \
"systemctl disable zivpn.service zivpn-api.service zivpn-bot.service zivpn-backup.service 2>/dev/null"

# ========= KILL ORPHAN PROCESSES =========
run_silent "Killing processes" \
"pkill -f zivpn 2>/dev/null; pkill -f zivpn-api 2>/dev/null; pkill -f zivpn-bot 2>/dev/null; pkill -f zivpn-backup 2>/dev/null"

# ========= REMOVE SYSTEMD FILES =========
run_silent "Removing systemd files" \
"rm -f /etc/systemd/system/zivpn.service \
       /etc/systemd/system/zivpn-api.service \
       /etc/systemd/system/zivpn-bot.service \
       /etc/systemd/system/zivpn-backup.service"

# ========= REMOVE MAIN DIRECTORY =========
run_silent "Removing ZiVPN directory" \
"rm -rf /etc/zivpn /usr/local/bin/zivpn"

# ========= REMOVE RCLONE CONFIG =========
run_silent 'Removing rclone config' \
"rm -rf /root/.config/rclone /root/.config/rclone/rclone.conf"

# ========= REMOVE IPTABLES RULES =========
run_silent "Cleaning iptables rules" \
"iptables -t nat -D PREROUTING -p udp --dport 6000:19999 -j DNAT --to-destination :5667 2>/dev/null"

# ========= REMOVE CRON JOBS =========
run_silent "Removing cron jobs" \
"rm -f /etc/cron.d/zivpn-autobackup /etc/cron.d/zivpn-*"

# ========= SYSTEMD CLEANUP =========
run_silent "Reloading systemd" \
"systemctl daemon-reload; systemctl reset-failed"

# ========= CLEAN CACHE =========
run_silent "Cleaning cache" \
"echo 3 > /proc/sys/vm/drop_caches 2>/dev/null"

# ========= LOG CLEANUP =========
if [ -f /tmp/zivpn_uninstall.log ]; then
  cp /tmp/zivpn_uninstall.log /tmp/zivpn_uninstall_backup.log
  rm -f /tmp/zivpn_uninstall.log
fi

echo ""
echo -e "${BOLD}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${RESET}"
echo -e "${GREEN}✅ Uninstallation Complete${RESET}"
echo -e "${BOLD}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${RESET}"
echo ""
echo -e "${CYAN}📋 Removed Components:${RESET}"
echo -e "  ❌ /etc/zivpn (All configs, API, bot, backup)"
echo -e "  ❌ /usr/local/bin/zivpn (binary)"
echo -e "  ❌ All systemd services"
echo -e "  ❌ All iptables rules"
echo -e "  ❌ All cron jobs"
echo -e "  ❌ rclone configuration"
echo ""
echo -e "${GREEN}✨ ZiVPN has been completely removed.${RESET}"