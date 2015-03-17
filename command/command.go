package command

import (
	"database/sql"
	"encoding/csv"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql" // required as SQL driver at the time of connection

	"github.com/awslabs/aws-sdk-go/aws"
	"github.com/awslabs/aws-sdk-go/gen/rds"

	"github.com/uchimanajet7/rds-try/config"
	"github.com/uchimanajet7/rds-try/logger"
	"github.com/uchimanajet7/rds-try/query"
	"github.com/uchimanajet7/rds-try/utils"
)

type CmdInterface interface {
	// return long help string
	Help() string

	// run command with arguments
	Run(args []string) int

	// return short help string
	Synopsis() string
}

type Command struct {
	OutConfig config.OutConfig
	RDSConfig config.RDSConfig
	RDSClient *rds.RDS
	ARNPrefix string
}

var log = logger.GetLogger("command")

var (
	ErrDBInstancetNotFound = errors.New("DB Instance is not found")
	ErrSnapshotNotFound    = errors.New("DB　Snapshot is not found")
	ErrDriverNotFound      = errors.New("DB　Driver is not found")
	ErrRdsTypesNotFound    = errors.New("RDS Types is not found")
	ErrRdsARNsNotFound     = errors.New("RDS ARN Types is not found")
)

func (c *Command) describeDBInstances(message *rds.DescribeDBInstancesMessage) ([]rds.DBInstance, error) {
	resp, err := c.RDSClient.DescribeDBInstances(message)

	if err != nil {
		log.Errorf("%s", err.Error())
		return nil, err
	}

	return resp.DBInstances, err
}

// all status in target, result return only one
func (c *Command) DescribeDBInstance(dbIdentifier string) (*rds.DBInstance, error) {
	// set DB ID
	message := &rds.DescribeDBInstancesMessage{
		DBInstanceIdentifier: aws.String(dbIdentifier),
	}
	resp, err := c.describeDBInstances(message)

	if err != nil {
		return nil, err
	}

	db_len := len(resp)
	if db_len < 1 {
		log.Errorf("%s", ErrDBInstancetNotFound.Error())
		return nil, ErrDBInstancetNotFound
	}

	return &resp[db_len-1], err
}

func (c *Command) checkListTagsForResourceMessage(rdstypes interface{}) (bool, error) {
	// want to filter by tag name and value
	// see also
	// ListTagsForResource - Amazon Relational Database Service
	// http://docs.aws.amazon.com/AmazonRDS/latest/APIReference/API_ListTagsForResource.html

	// get tag list
	state := false
	tag_resp, err := c.RDSClient.ListTagsForResource(
		&rds.ListTagsForResourceMessage{
			ResourceName: aws.String(c.getARNString(rdstypes)),
		})
	if err != nil {
		log.Errorf("%s", err.Error())
		return state, err
	}
	if len(tag_resp.TagList) <= 0 {
		return state, err
	}

	// check tag name and value
	tag_cnt := 0
	for _, tag := range tag_resp.TagList {
		switch *tag.Key {
		case rt_name_text:
			// if the rt_name tag exists, should the prefix value has become an application name
			if strings.HasPrefix(*tag.Value, utils.GetFormatedAppName()) {
				tag_cnt++
			}
		case rt_time_text:
			// rt_time tag exists
			tag_cnt++
		}
	}

	// to-do: Fixed value the number you are using to the judgment has been hard-coded
	if tag_cnt >= 2 {
		state = true
		return state, err
	}

	return state, err
}

func (c *Command) DescribeDBInstancesByTags() ([]rds.DBInstance, error) {
	message := &rds.DescribeDBInstancesMessage{}

	resp, err := c.describeDBInstances(message)

	if err != nil {
		return nil, err
	}

	var dbInstances []rds.DBInstance
	for _, instance := range resp {
		state, err := c.checkListTagsForResourceMessage(instance)
		if err != nil {
			return nil, err
		}

		if state {
			dbInstances = append(dbInstances, instance)
		}
	}

	return dbInstances, err
}

func (c *Command) ModifyDBInstance(dbIdentifier string, dbInstance *rds.DBInstance) (*rds.DBInstance, error) {
	var vpc_ids []string
	for _, vpc_id := range dbInstance.VPCSecurityGroups {
		vpc_ids = append(vpc_ids, *vpc_id.VPCSecurityGroupID)
	}

	message := &rds.ModifyDBInstanceMessage{
		DBInstanceIdentifier: aws.String(dbIdentifier),
		DBParameterGroupName: aws.String(*dbInstance.DBParameterGroups[0].DBParameterGroupName),
		VPCSecurityGroupIDs:  vpc_ids,
		ApplyImmediately:     aws.Boolean(true), // "ApplyImmediately" is always true
	}

	resp, err := c.RDSClient.ModifyDBInstance(message)

	if err != nil {
		log.Errorf("%s", err.Error())
		return nil, err
	}

	return resp.DBInstance, err
}

func (c *Command) RebootDBInstance(dbIdentifier string) (*rds.DBInstance, error) {
	message := &rds.RebootDBInstanceMessage{
		DBInstanceIdentifier: aws.String(dbIdentifier),
	}

	resp, err := c.RDSClient.RebootDBInstance(message)

	if err != nil {
		log.Errorf("%s", err.Error())
		return nil, err
	}

	return resp.DBInstance, err
}

type RestoreDBInstanceFromDBSnapshotArgs struct {
	DBInstanceClass string
	DBIdentifier    string
	MultiAZ         bool
	Snapshot        *rds.DBSnapshot
	Instance        *rds.DBInstance
}

func (c *Command) RestoreDBInstanceFromDBSnapshot(args *RestoreDBInstanceFromDBSnapshotArgs) (*rds.DBInstance, error) {
	message := &rds.RestoreDBInstanceFromDBSnapshotMessage{
		DBInstanceClass:      aws.String(args.DBInstanceClass),
		DBInstanceIdentifier: aws.String(args.DBIdentifier),
		MultiAZ:              aws.Boolean(args.MultiAZ),
		DBSnapshotIdentifier: args.Snapshot.DBSnapshotIdentifier,
		DBSubnetGroupName:    args.Instance.DBSubnetGroup.DBSubnetGroupName,
		StorageType:          args.Instance.StorageType,
		Tags:                 getSpecifyTags(), // It must always be set to not forget
	}

	resp, err := c.RDSClient.RestoreDBInstanceFromDBSnapshot(message)

	if err != nil {
		log.Errorf("%s", err.Error())
		return nil, err
	}

	return resp.DBInstance, err
}

func (c *Command) DescribeDBSnapshotsByTags() ([]rds.DBSnapshot, error) {
	message := &rds.DescribeDBSnapshotsMessage{}

	resp, err := c.describeDBSnapshots(message)

	if err != nil {
		return nil, err
	}

	var dbSnapshots []rds.DBSnapshot
	for _, snapshot := range resp {
		state, err := c.checkListTagsForResourceMessage(snapshot)
		if err != nil {
			return nil, err
		}

		if state {
			dbSnapshots = append(dbSnapshots, snapshot)
		}
	}

	return dbSnapshots, err
}

func (c *Command) describeDBSnapshots(message *rds.DescribeDBSnapshotsMessage) ([]rds.DBSnapshot, error) {
	resp, err := c.RDSClient.DescribeDBSnapshots(message)

	if err != nil {
		log.Errorf("%s", err.Error())
		return nil, err
	}

	return resp.DBSnapshots, err
}

// the target only "available"
func (c *Command) DescribeLatestDBSnapshot(dbIdentifier string) (*rds.DBSnapshot, error) {
	message := &rds.DescribeDBSnapshotsMessage{
		DBInstanceIdentifier: aws.String(dbIdentifier),
	}

	resp, err := c.describeDBSnapshots(message)

	if err != nil {
		return nil, err
	}

	// want to filter by status "available"
	var dbSnapshots []rds.DBSnapshot
	for _, snapshot := range resp {
		if *snapshot.Status != "available" {
			log.Debugf("DB Snapshot Status : %s", *snapshot.Status)
			continue
		}

		dbSnapshots = append(dbSnapshots, snapshot)
	}

	db_len := len(dbSnapshots)
	if db_len < 1 {
		log.Errorf("%s", ErrSnapshotNotFound.Error())
		return nil, ErrSnapshotNotFound
	}

	return &dbSnapshots[db_len-1], err
}

// all status in target, result return only one
func (c *Command) DescribeDBSnapshot(snapshotIdentifier string) (*rds.DBSnapshot, error) {
	message := &rds.DescribeDBSnapshotsMessage{
		DBSnapshotIdentifier: aws.String(snapshotIdentifier),
	}

	resp, err := c.describeDBSnapshots(message)

	if err != nil {
		return nil, err
	}

	db_len := len(resp)
	if db_len < 1 {
		log.Errorf("%s", ErrSnapshotNotFound.Error())
		return nil, ErrSnapshotNotFound
	}

	return &resp[db_len-1], err
}

// delete DB instance and skip create snapshot
func (c *Command) DeleteDBInstance(dbIdentifier string) (*rds.DBInstance, error) {
	message := &rds.DeleteDBInstanceMessage{
		DBInstanceIdentifier: aws.String(dbIdentifier),
		SkipFinalSnapshot:    aws.Boolean(true), // "SkipFinalSnapshot" is always true
	}

	resp, err := c.RDSClient.DeleteDBInstance(message)

	if err != nil {
		log.Errorf("%s", err.Error())
		return nil, err
	}

	return resp.DBInstance, err
}

func (c *Command) CreateDBSnapshot(dbIdentifier string) (*rds.DBSnapshot, error) {
	message := &rds.CreateDBSnapshotMessage{
		DBInstanceIdentifier: aws.String(dbIdentifier),
		DBSnapshotIdentifier: aws.String(utils.GetFormatedDBDisplayName(dbIdentifier)),
		Tags:                 getSpecifyTags(), // It must always be set to not forget
	}

	resp, err := c.RDSClient.CreateDBSnapshot(message)

	if err != nil {
		log.Errorf("%s", err.Error())
		return nil, err
	}

	return resp.DBSnapshot, err
}

func (c *Command) DeleteDBSnapshot(snapshotIdentifier string) (*rds.DBSnapshot, error) {
	message := &rds.DeleteDBSnapshotMessage{
		DBSnapshotIdentifier: aws.String(snapshotIdentifier),
	}

	resp, err := c.RDSClient.DeleteDBSnapshot(message)

	if err != nil {
		log.Errorf("%s", err.Error())
		return nil, err
	}

	return resp.DBSnapshot, err
}

// "Pending Status" If the return value is ture
func (c *Command) CheckPendingStatus(dbInstance *rds.DBInstance) bool {
	for _, item := range dbInstance.DBParameterGroups {
		if *item.ParameterApplyStatus != "in-sync" {
			return true
		}
	}

	for _, item := range dbInstance.VPCSecurityGroups {
		if *item.Status != "active" {
			return true
		}
	}

	return false
}

func (c *Command) DeleteDBResources(rdstypes interface{}) error {

	switch rdstype := rdstypes.(type) {
	case []rds.DBSnapshot:
		for i, item := range rdstype {
			resp, err := c.DeleteDBSnapshot(*item.DBSnapshotIdentifier)
			if err != nil {
				return err
			}
			log.Infof("[% d] deleted DB Snapshot: %s", i+1, *resp.DBSnapshotIdentifier)
		}
	case []rds.DBInstance:
		for i, item := range rdstype {
			resp, err := c.DeleteDBInstance(*item.DBInstanceIdentifier)
			if err != nil {
				return err
			}
			log.Infof("[% d] deleted DB Instance: %s", i+1, *resp.DBInstanceIdentifier)
		}
	default:
		log.Errorf("%s", ErrRdsTypesNotFound.Error())
	}

	return nil
}

// wait for status available
func (c *Command) WaitForStatusAvailable(rdstypes interface{}) <-chan bool {
	receiver := make(chan bool)
	// 30 seconds intervals checked
	ticker := time.NewTicker(30 * time.Second)
	// 30 minutes time out
	timeout := time.After(30 * time.Minute)

	go func() {
		for {
			select {
			case tick := <-ticker.C:
				var rds_status string

				log.Debugf("tick: %s", tick)

				switch rdstype := rdstypes.(type) {
				case *rds.DBSnapshot:
					db_snapshot, err := c.DescribeDBSnapshot(*rdstype.DBSnapshotIdentifier)

					if err != nil {
						receiver <- false

						ticker.Stop()
					}

					rds_status = *db_snapshot.Status
					log.Infof("DB Snapshot Status: %s", rds_status)
				case *rds.DBInstance:
					db_instance, err := c.DescribeDBInstance(*rdstype.DBInstanceIdentifier)

					if err != nil {
						receiver <- false

						ticker.Stop()
					}

					rds_status = *db_instance.DBInstanceStatus
					log.Infof("DB Instance Status: %s", rds_status)
				default:
					log.Errorf("%s", ErrRdsTypesNotFound.Error())
				}

				if rds_status == "available" {
					receiver <- true
					log.Infof("Status: %s", rds_status)

					ticker.Stop()
				}
			case out := <-timeout:
				receiver <- false
				log.Infof("time out: %s", out)

				ticker.Stop()
			}
		}
	}()

	return receiver
}

type ExecuteSQLArgs struct {
	Engine   string // rds engine name
	Endpoint *rds.Endpoint
	Queries  []query.Query
}

func (c *Command) ExecuteSQL(args *ExecuteSQLArgs) ([]time.Duration, error) {
	driver, dsn := c.getDbOpenValues(args)

	if driver == "" {
		log.Errorf("%s", ErrDriverNotFound.Error())
		return nil, ErrDriverNotFound
	}

	db, err := sql.Open(driver, dsn)
	if err != nil {
		log.Errorf("%s", err.Error())
		return nil, err
	}
	defer db.Close()

	times := make([]time.Duration, 0, len(args.Queries))
	for _, value := range args.Queries {
		log.Debugf("query value : %s", value)

		s_time := time.Now()
		log.Infof("query start time: %s", s_time)

		result, err := db.Query(value.Sql)
		if err != nil {
			log.Errorf("%s", err.Error())
			return times, err
		}

		e_time := time.Now()
		log.Infof("query end time: %s", e_time)

		times = append(times, e_time.Sub(s_time))

		// output csv file
		cols, _ := result.Columns()
		if c.OutConfig.File && len(cols) > 0 {
			file_name := value.Name + "-" + utils.GetFormatedTime() + ".csv"
			out_path := utils.GetHomeDir()
			if c.OutConfig.Root != "" {
				out_path = c.OutConfig.Root
			}

			out_state := writeCSVFile(
				&writeCSVFileArgs{
					Rows:     result,
					FileName: file_name,
					Path:     out_path,
					Bom:      c.OutConfig.Bom,
				})
			log.Debugf("out_state:%+v", out_state)
		}

		result.Close()
	}

	return times, nil
}

func (c *Command) getDbOpenValues(args *ExecuteSQLArgs) (string, string) {
	var driver_name string
	var data_source_name string

	engine := strings.ToLower(args.Engine)
	log.Debugf("aws engine name: %s", engine)

	// convert from "aws engine name" to "golang db driver name"
	// see also
	// CreateDBInstance - Amazon Relational Database Service
	// http://docs.aws.amazon.com/AmazonRDS/latest/APIReference/API_CreateDBInstance.html
	// Request Parameters "Engine" is Valid Values
	// Valid Values: MySQL | oracle-se1 | oracle-se | oracle-ee | sqlserver-ee | sqlserver-se | sqlserver-ex | sqlserver-web | postgres
	//
	// SQLDrivers · golang/go Wiki · GitHub
	// https://github.com/golang/go/wiki/SQLDrivers
	//
	// to-do: correspondence of mysql only
	switch {
	case strings.Contains(engine, "mysql"):
		driver_name = "mysql"
		data_source_name = fmt.Sprintf("%s:%s@tcp(%s:%d)/", c.RDSConfig.User, c.RDSConfig.Pass, *args.Endpoint.Address, *args.Endpoint.Port)
	case strings.Contains(engine, "oracle"):
		driver_name = "oracle"
	case strings.Contains(engine, "sqlserver"):
		driver_name = "sqlserver"
	case strings.Contains(engine, "postgres"):
		driver_name = "postgres"
	default:
		log.Errorf("failed to convert. no matching SQL driver: %s", engine)
	}

	log.Debugf("golang db driver name: %s", driver_name)
	log.Debugf("golang db data source name: %s", data_source_name)

	return driver_name, data_source_name
}

func (c *Command) getARNString(rdstypes interface{}) string {
	// edit ARN string
	// see also
	// Tagging Amazon RDS Resources - Amazon Relational Database Service
	// http://docs.aws.amazon.com/AmazonRDS/latest/UserGuide/USER_Tagging.html

	var arn string
	switch rdstype := rdstypes.(type) {
	case rds.DBSnapshot:
		arn = c.ARNPrefix + "snapshot:" + *rdstype.DBSnapshotIdentifier
	case rds.DBInstance:
		arn = c.ARNPrefix + "db:" + *rdstype.DBInstanceIdentifier
	default:
		log.Errorf("%s", ErrRdsARNsNotFound.Error())
	}

	log.Debugf("ARN: %s", arn)

	return arn
}

const rt_name_text = "rt_name"
const rt_time_text = "rt_time"

// use the tag for identification
func getSpecifyTags() []rds.Tag {
	var tag_list []rds.Tag
	var tag rds.Tag

	// append name
	tag = rds.Tag{
		Key:   aws.String(rt_name_text),
		Value: aws.String(utils.GetFormatedAppName()),
	}
	tag_list = append(tag_list, tag)

	// append time
	tag = rds.Tag{
		Key:   aws.String(rt_time_text),
		Value: aws.String(utils.GetFormatedTime()),
	}
	tag_list = append(tag_list, tag)

	return tag_list
}

type writeCSVFileArgs struct {
	Rows     *sql.Rows
	FileName string
	Path     string
	Bom      bool
}

func writeCSVFile(args *writeCSVFileArgs) bool {
	const BOM = string('\uFEFF')

	cols, err := args.Rows.Columns()
	if err != nil {
		log.Errorf("%s", err.Error())
		return false
	}

	// is append bom?
	if args.Bom {
		// When making the extension a txt, UTF8 can be used in Excel.
		args.FileName = fmt.Sprintf("utf8-bom_%s", args.FileName)
	}
	out_path := path.Join(args.Path, args.FileName)

	// all user access OK
	file, err := os.OpenFile(out_path, os.O_WRONLY|os.O_CREATE, 0777)
	defer file.Close()

	// set empty
	err = file.Truncate(0)

	// write csv
	writer := csv.NewWriter(file)

	// add BOM
	if args.Bom {
		boms := make([]string, 1)
		boms[0] = BOM + fmt.Sprintf("# character encoding : utf-8 with BOM")
		writer.Write(boms)
	}

	// write header
	writer.Write(cols)

	// Result is your slice string.
	rawResult := make([][]byte, len(cols))
	result := make([]string, len(cols))

	// A temporary interface{} slice
	dest := make([]interface{}, len(cols))
	// Put pointers to each string in the interface slice
	for i, _ := range rawResult {
		dest[i] = &rawResult[i]
	}

	for args.Rows.Next() {
		err = args.Rows.Scan(dest...)
		if err != nil {
			log.Errorf("%s", err.Error())
		}

		for i, raw := range rawResult {
			if raw == nil {
				result[i] = "null"
			} else {
				result[i] = string(raw)
			}
		}
		writer.Write(result)
	}
	writer.Flush()

	return true
}