workspace "KestrelDesk" "Current self-hosted application architecture" {
  model {
    customer = person "Customer" "Uses customer support pages or an embedded chat surface."
    employee = person "Employee / Admin" "Uses the employee workspace."

    kestrel = softwareSystem "KestrelDesk" "Self-hosted customer support and employee operations platform." {
      web = container "Web UI and embedded SDK" "Customer support and employee workspace delivered to untrusted browsers." "Next.js / React / TypeScript / Web SDK"
      app = container "Go application / API" "Server-side authorization boundary, REST and WebSocket APIs, services and AI runtime." "Go / Gin / GORM"
      relational = container "Relational data" "MySQL is the default; SQLite is supported." "MySQL / SQLite / GORM"
      vector = container "Vector backend" "Qdrant is the default; LanceDB is an optional vector backend." "Qdrant / LanceDB"
    }

    llm = softwareSystem "LLM providers" "External model providers configured by the operator."
    embedding = softwareSystem "Embedding providers" "External embedding providers configured by the operator."
    email = softwareSystem "SMTP / email" "Configured outbound email service."
    storage = softwareSystem "Local / OSS storage" "Configured file and object storage."
    channels = softwareSystem "Channels / integrations" "Web, WeChat, WeCom, Telegram, Slack, Discord, Lark, Zalo, and MCP servers or tools."

    customer -> web "Uses customer chat"
    employee -> web "Uses employee workspace"
    web -> app "HTTPS · REST · WebSocket"
    app -> relational "Uses configured relational driver"
    app -> vector "Retrieves evidence when enabled"
    app -> llm "Sends configured model requests"
    app -> embedding "Embeds documents and queries when configured"
    app -> email "Sends configured email"
    app -> storage "Reads and writes configured files"
    app -> channels "Receives and sends channel events"
  }

  views {
    systemContext kestrel "kestreldesk-context" "KestrelDesk system context" {
      include *
      autoLayout lr
    }
    container kestrel "kestreldesk-containers" "KestrelDesk container view" {
      include *
      autoLayout lr
    }
    styles {
      element "Person" {
        shape person
        background #64748b
        color #ffffff
      }
      element "Software System" {
        background #f1f5f9
        color #1e293b
        stroke #64748b
      }
      element "Container" {
        background #dbeafe
        color #1e3a8a
        stroke #2563eb
      }
    }
  }
}
