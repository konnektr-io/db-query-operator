nektr Platform - Application Scope & Boundaries

## 🎯 Overview

This document defines the scope, responsibilities, and boundaries for each application within the Konnektr Platform to prevent overlapping functionality, reduce development friction, and ensure all components integrate into a cohesive, scalable, and maintainable system.

## 🏗️ Platform Architecture

### Control Plane vs Data Plane

The Konnektr Platform is divided into two distinct logical planes:

**Control Plane (KtrlPlane, DB Query Operator)**

- Manages lifecycle of all resources, users, organizations, billing, and access control
- Central administrative backbone of the entire platform
- Provides centralized authentication and authorization

**Data Plane (Products: Graph, Flow, Assembler, Compass)**

- User-facing products for building and running digital twin solutions
- Tenants of the Control Plane
- Focus on domain-specific functionality

## 📋 Guiding Principles

### 1. Separation of Concerns

Each application has a single primary responsibility and excels at that function.

### 2. Decoupled Architecture

- No direct knowledge of other applications' internal logic or databases
- All interactions through well-defined, versioned REST/gRPC APIs
- Asynchronous communication via message bus (Dapr)

### 3. Declarative Operations

- Data Plane applications make declarative requests to KtrlPlane API
- KtrlPlane and DB Query Operator reconcile desired vs actual state
- No direct infrastructure management by product applications

### 4. Centralized Identity & Access Management

- All authentication/authorization managed by KtrlPlane
- Other applications are clients of KtrlPlane's IAM service
- No separate user tables in product databases

### 5. Consistent User Experience

- All applications use shared Konnektr Design System (shadcn/vite)
- Seamless experience across the platform

## 🏢 Application Scopes

### KtrlPlane (Control Plane)

**One-Liner**: The central Control Plane for managing all users, resources, and billing.

**✅ In-Scope Features:**

- User sign-up, sign-in, and profile management
- Organization, Project, and Resource CRUD operations
- Role-Based Access Control (RBAC) at organization and project level
- Billing account management, subscription logic, and invoicing integration
- Central user dashboard for viewing and navigating to all resources
- Konnektr Platform API for all management tasks
- Resource lifecycle management across all products

**❌ Out-of-Scope:**

- Internal business logic of any products (graph queries, flow execution, etc.)
- Direct deployment of infrastructure (handled by DB Query Operator)
- Product-specific UI functionality

**🔌 Interactions:**

- **All Applications**: Provides central authentication service
- **DB Query Operator**: Writes desired state to database for reconciliation
- **Product Applications**: Consume KtrlPlane API for resource management

---

### DB Query Operator (Infrastructure)

**One-Liner**: Kubernetes operator that reconciles desired state from database with cluster state.

**✅ In-Scope:**

- Watch database tables for state changes
- Create/update/delete Kubernetes resources based on database records
- Update resource status in database upon reconciliation

**❌ Out-of-Scope:**

- Business logic, user interaction, or API exposure

---

### Konnektr Home Page (Marketing) - **THIS APPLICATION**

**One-Liner**: Public-facing marketing and information website.

**✅ In-Scope:**

- Product information, pricing, blog, documentation links
- Interactive demos and marketing content
- Lead generation and conversion optimization
- SEO optimization and organic discovery
- Brand messaging and value proposition communication
- User journey from discovery to platform engagement

**❌ Out-of-Scope:**

- User authentication or user-specific data
- Resource management functionality
- Billing or subscription management
- Direct product functionality implementation

**🔌 Key Interactions:**

- Redirects to KtrlPlane for authentication and resource creation
- Supports deep linking to specific product creation flows
- Integration with KtrlPlane URL patterns for seamless transitions

---

### Konnektr Documentation Site (docs.konnektr.com)

**One-Liner**: Central portal for all user-facing Konnektr documentation, guides, and API references.

**✅ In-Scope:**
- Interactive, searchable docs for all products using MDX, React, Vite, and Shadcn/UI
- Consistent branding and design system
- Automated PRs from product repos merge user docs into the site

**❌ Out-of-Scope:**
- Marketing, sales, or lead generation (see Konnektr Home)
- Product business logic, backend integration, or user authentication

**Key Interactions:**
- "Docs" links from all apps point to docs.konnektr.com
- Product repos push doc changes via GitHub Actions/PRs

**Architecture:**
- Separate Vite/React/MDX/Shadcn project (konnektr-docs)
- Content organized by product, with live React components for demos
- Deployed to docs.konnektr.com via static hosting/CDN

---

### Konnektr Assembler (AI Builder)

**One-Liner**: AI-powered builder for creating digital twin models.

**✅ In-Scope:**

- Data source connection UI
- AI-powered DTDL model generation
- Visual graph editor for model refinement
- Model validation and deployment workflow

**❌ Out-of-Scope:**

- Direct infrastructure deployment
- Long-term data storage
- Running the final digital twin

**🔌 Key Interaction:**

- Calls KtrlPlane API to request creation of Graph and Flow resources

---

### Konnektr Graph (formerly AgeDigitalTwins)

**One-Liner**: High-performance, ADT-compatible digital twin runtime and graph database.

**✅ In-Scope:**

- ADT-compatible REST API for twin operations
- Real-time eventing system for state changes
- Query execution engine (Cypher over Apache AGE)

**❌ Out-of-Scope:**

- User authentication (validates KtrlPlane tokens)
- Billing or self-management
- UI components

**🔌 Key Interactions:**

- Validates JWTs from KtrlPlane
- Consumed by Flow and Compass applications

---

### Konnektr Flow (Data Processing)

**One-Liner**: Real-time data ingestion and event orchestration engine.

**✅ In-Scope:**

- Visual workflow editor
- Data source connectors library
- Serverless runtime for flow execution
- Flow monitoring and logging

**❌ Out-of-Scope:**

- Primary twin graph storage
- Direct resource provisioning

**🔌 Key Interactions:**

- Authenticates via KtrlPlane
- Primary client of Konnektr Graph API

---

### Konnektr Compass (Analytics)

**One-Liner**: Analytics, visualization, and simulation layer.

**✅ In-Scope:**

- Dashboarding and visualization tools
- "What-if" simulation engine
- Cross-twin analytical queries

**❌ Out-of-Scope:**

- Twin lifecycle management
- Data ingestion

**🔌 Key Interactions:**

- Uses KtrlPlane for permissions
- Queries Konnektr Graph for analytics

## 🚨 Scope Enforcement Rules

### For Konnektr Home Development:

**✅ Always Do:**

- Focus on marketing and lead generation
- Create compelling product information and demos
- Optimize for SEO and organic discovery
- Implement clear user journeys to platform engagement
- Maintain consistent brand messaging
- Redirect to KtrlPlane for authentication and resource management

**❌ Never Do:**

- Implement user authentication or session management
- Store user-specific data or create user accounts
- Implement business logic of other Konnektr products
- Create separate billing or subscription systems
- Directly integrate with product databases
- Build resource management functionality

### Cross-Application Communication:

**✅ Allowed:**

- Static links to other applications
- URL parameter passing for deep linking
- Shared design system components
- Marketing analytics and tracking

**❌ Forbidden:**

- Direct API calls to product applications
- Shared authentication sessions
- Direct database connections
- Real-time data integration

## 🔄 Boundary Evolution

When requirements blur these boundaries:

1. **First**: Try to solve within existing scope
2. **Second**: Create clear handoff points to appropriate applications
3. **Last Resort**: Discuss boundary changes with architecture team

## 📝 Compliance Checklist

Before implementing any feature, verify:

- [ ] Does this belong in Konnektr Home's marketing scope?
- [ ] Am I maintaining separation from platform functionality?
- [ ] Are all user management features redirected to KtrlPlane?
- [ ] Does this enhance the marketing and discovery experience?
- [ ] Will this support seamless transitions to the platform?

---

## 📋 Change Log

- **2025-01-27**: Initial platform scope document created
- **2025-09-25**: Updated product names (AgeDigitalTwins → Konnektr Graph, clarified product descriptions), added Konnektr Home as THIS APPLICATION
- **2025-09-25**: Enhanced Konnektr Home scope definition for marketing website focus

---

**Remember**: These boundaries exist to prevent chaos and technical debt. When in doubt, choose the more restrictive interpretation and discuss with the team.
