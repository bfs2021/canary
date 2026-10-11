**Canary — portfolio, risk and order tools for Interactive Brokers**

Canary runs locally with TWS or IB Gateway, providing portfolio monitoring, market analysis, risk checks, performance review and order management. Order submission requires the trading build and its approval checks. Standard downloads and MCP remain read-only.

## New in __VERSION__

__HIGHLIGHTS__

## Install

For the standard read-only CLI and local MCP server:

~~~sh
curl -fsSL https://raw.githubusercontent.com/osauer/canary/main/install.sh | sh
~~~

For Claude Desktop, download `canary.mcpb` from the assets below. For order submission, choose a `canary-trading-__VERSION__-*` archive and follow the [trading guide](https://github.com/osauer/canary/blob/__VERSION__/docs/docs/operate/orders.md) and [security guidance](https://github.com/osauer/canary/blob/__VERSION__/SECURITY.md).

Requires a local TWS or IB Gateway session on macOS, Linux or Windows through WSL. See the [installation guide](https://github.com/osauer/canary/blob/__VERSION__/docs/docs/start/install.md) for setup, signature verification and other MCP clients.

__GO_INSTALL__

## Detailed changes
