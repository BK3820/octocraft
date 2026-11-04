// Request persistence: Azure Table Storage when STORAGE_ACCOUNT_NAME is set, else a local JSON file.
const fs = require('fs');
const path = require('path');

const PARTITION = 'request';

function localStore(dir) {
  const file = path.join(dir, 'requests.json');
  fs.mkdirSync(dir, { recursive: true });
  const load = () => (fs.existsSync(file) ? JSON.parse(fs.readFileSync(file, 'utf8')) : []);
  const save = (rows) => fs.writeFileSync(file, JSON.stringify(rows, null, 2));
  return {
    kind: 'local',
    async list() {
      return load();
    },
    async get(id) {
      return load().find((r) => r.id === id) || null;
    },
    async insert(row) {
      const rows = load();
      rows.push(row);
      save(rows);
      return row;
    },
    async update(id, patch) {
      const rows = load();
      const row = rows.find((r) => r.id === id);
      if (!row) return null;
      Object.assign(row, patch);
      save(rows);
      return row;
    },
  };
}

function tableStore(account, tableName) {
  const { TableClient } = require('@azure/data-tables');
  const { DefaultAzureCredential } = require('@azure/identity');
  const client = new TableClient(`https://${account}.table.core.windows.net`, tableName, new DefaultAzureCredential());
  let ready;
  const ensure = () => (ready ||= client.createTable().catch((e) => (e.statusCode === 409 ? null : Promise.reject(e))));
  const toEntity = (row) => ({ partitionKey: PARTITION, rowKey: row.id, status: row.status, data: JSON.stringify(row) });

  return {
    kind: 'azure-table',
    async list() {
      await ensure();
      const rows = [];
      for await (const e of client.listEntities({ queryOptions: { filter: `PartitionKey eq '${PARTITION}'` } })) {
        rows.push(JSON.parse(e.data));
      }
      return rows.sort((a, b) => a.created_at.localeCompare(b.created_at));
    },
    async get(id) {
      await ensure();
      try {
        return JSON.parse((await client.getEntity(PARTITION, id)).data);
      } catch (e) {
        if (e.statusCode === 404) return null;
        throw e;
      }
    },
    async insert(row) {
      await ensure();
      await client.createEntity(toEntity(row));
      return row;
    },
    async update(id, patch) {
      const row = await this.get(id);
      if (!row) return null;
      Object.assign(row, patch);
      await client.upsertEntity(toEntity(row), 'Replace');
      return row;
    },
  };
}

function createStore(env = process.env) {
  if (env.STORAGE_ACCOUNT_NAME) return tableStore(env.STORAGE_ACCOUNT_NAME, env.REQUESTS_TABLE || 'apprequests');
  return localStore(env.DATA_DIR || path.join(__dirname, '..', 'data'));
}

module.exports = { createStore, localStore };
