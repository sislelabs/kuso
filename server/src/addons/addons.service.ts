import { Injectable } from '@nestjs/common';
import { IPlugin } from './plugins/plugin.interface';
import { KusoMysql } from './plugins/kusoMysql';
import { KusoRedis } from './plugins/kusoRedis';
import { KusoPostgresql } from './plugins/kusoPostgresql';
import { KusoMongoDB } from './plugins/kusoMongoDB';
import { KusoMemcached } from './plugins/kusoMemcached';
import { KusoElasticsearch } from './plugins/kusoElasticsearch';
import { KusoCouchDB } from './plugins/kusoCouchDB';
import { KusoKafka } from './plugins/kusoKafka';
import { KusoMail } from './plugins/kusoMail';
import { KusoRabbitMQ } from './plugins/kusoRabbitMQ';
import { Tunnel } from './plugins/cloudflare';
import { PostgresCluster } from './plugins/postgresCluster';
import { RedisCluster } from './plugins/redisCluster';
import { Redis } from './plugins/redis';
import { PerconaServerMongoDB as MongoDB } from './plugins/mongoDB';
import { Cockroachdb } from './plugins/cockroachDB';
import { Tenant } from './plugins/minio';
import { ClickHouseInstallation } from './plugins/clickhouse';
import { KubernetesService } from '../kubernetes/kubernetes.service';
import { KusoAddonPostgres } from './plugins/kusoaddonsPostgres';
import { KusoAddonMysql } from './plugins/kusoaddonsMysql';
import { KusoAddonRedis } from './plugins/kusoaddonsRedis';
import { KusoAddonRabbitmq } from './plugins/kusoaddonsRabbitmq';
import { KusoAddonMongodb } from './plugins/kusoaddonsMongodb';
import { KusoAddonMemcached } from './plugins/kusoaddonsMemcached';
import { Cluster as CloudnativePG } from './plugins/cloudnativePG';
import { Elasticsearch } from './plugins/elasticsearch';

@Injectable()
export class AddonsService {
  private operatorsAvailable: string[] = [];
  public addonsList: IPlugin[] = []; // List or possibly installed operators
  private CRDList: any; //List of installed CRDs from kubectl

  constructor(private kubectl: KubernetesService) {
    this.loadOperators();
  }

  public async loadOperators(): Promise<void> {
    // Load all Custom Resource Definitions to get the list of installed operators
    this.CRDList = await this.kubectl.getCustomresources();


    const kusoAddonPostgres = new KusoAddonPostgres(this.CRDList);
    this.addonsList.push(kusoAddonPostgres);

    const kusoAddonRedis = new KusoAddonRedis(this.CRDList);
    this.addonsList.push(kusoAddonRedis);
    
    const kusoAddonMysql = new KusoAddonMysql(this.CRDList);
    this.addonsList.push(kusoAddonMysql);

    const kusoAddonMemcached = new KusoAddonMemcached(this.CRDList);
    this.addonsList.push(kusoAddonMemcached);

    const kusoAddonMongodb = new KusoAddonMongodb(this.CRDList);
    this.addonsList.push(kusoAddonMongodb);

    const kusoCouchDB = new KusoCouchDB(this.CRDList);
    this.addonsList.push(kusoCouchDB);

    const kusoMail = new KusoMail(this.CRDList);
    this.addonsList.push(kusoMail);

    const kusoAddonRabbitMQ = new KusoAddonRabbitmq(this.CRDList);
    this.addonsList.push(kusoAddonRabbitMQ);

    const tunnel = new Tunnel(this.CRDList);
    this.addonsList.push(tunnel);

    const cloudnativePG = new CloudnativePG(this.CRDList);
    this.addonsList.push(cloudnativePG);

    const postgresCluster = new PostgresCluster(this.CRDList);
    this.addonsList.push(postgresCluster);

    const redisCluster = new RedisCluster(this.CRDList);
    this.addonsList.push(redisCluster);

    const redis = new Redis(this.CRDList);
    this.addonsList.push(redis);

    const elasticsearch = new Elasticsearch(this.CRDList);
    this.addonsList.push(elasticsearch);

    const mongoDB = new MongoDB(this.CRDList);
    this.addonsList.push(mongoDB);

    const cockroachdb = new Cockroachdb(this.CRDList);
    this.addonsList.push(cockroachdb);

    const minio = new Tenant(this.CRDList);
    this.addonsList.push(minio);

    const clickhouse = new ClickHouseInstallation(this.CRDList);
    this.addonsList.push(clickhouse);

    const kusoMysql = new KusoMysql(this.CRDList);
    this.addonsList.push(kusoMysql);

    const kusoRedis = new KusoRedis(this.CRDList);
    this.addonsList.push(kusoRedis);

    const kusoKafka = new KusoKafka(this.CRDList);
    this.addonsList.push(kusoKafka);

    const kusoMemcached = new KusoMemcached(this.CRDList);
    this.addonsList.push(kusoMemcached);

    const kusoElasticsearch = new KusoElasticsearch(this.CRDList);
    this.addonsList.push(kusoElasticsearch);

    const kusoMongoDB = new KusoMongoDB(this.CRDList);
    this.addonsList.push(kusoMongoDB);

    const kusoPostgresql = new KusoPostgresql(this.CRDList);
    this.addonsList.push(kusoPostgresql);

    const kusoRabbitMQ = new KusoRabbitMQ(this.CRDList);
    this.addonsList.push(kusoRabbitMQ);

  }

  public async getAddonsList(): Promise<IPlugin[]> {
    return this.addonsList;
  }

  public getOperatorsList(): string[] {
    return this.operatorsAvailable;
  }
}
