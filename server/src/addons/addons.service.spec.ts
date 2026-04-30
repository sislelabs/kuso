import { AddonsService } from './addons.service';

jest.mock('./plugins/kusoMysql');
jest.mock('./plugins/kusoRedis');
jest.mock('./plugins/kusoPostgresql');
jest.mock('./plugins/kusoMongoDB');
jest.mock('./plugins/kusoMemcached');
jest.mock('./plugins/kusoElasticsearch');
jest.mock('./plugins/kusoCouchDB');
jest.mock('./plugins/kusoKafka');
jest.mock('./plugins/kusoMail');
jest.mock('./plugins/kusoRabbitMQ');
jest.mock('./plugins/cloudflare');
jest.mock('./plugins/postgresCluster');
jest.mock('./plugins/redisCluster');
jest.mock('./plugins/redis');
jest.mock('./plugins/mongoDB');
jest.mock('./plugins/cockroachDB');
jest.mock('./plugins/minio');
jest.mock('./plugins/clickhouse');

describe('AddonsService', () => {
  let service: AddonsService;
  let kubectl: any;

  beforeEach(async () => {
    kubectl = {
      getCustomresources: jest.fn().mockResolvedValue({}),
    };
    service = new AddonsService(kubectl);
    await service.loadOperators();
  });

  it('should be defined', () => {
    expect(service).toBeDefined();
  });

  it('should load all addons into addonsList', async () => {
    expect(service.addonsList.length).toBeGreaterThan(0);
  });

  it('should return addons list', async () => {
    const list = await service.getAddonsList();
    expect(Array.isArray(list)).toBe(true);
    expect(list.length).toBe(service.addonsList.length);
  });

  it('should return operators list', () => {
    const ops = service.getOperatorsList();
    expect(Array.isArray(ops)).toBe(true);
    expect(ops).toEqual([]);
  });
});
