package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/golang/glog"
	"github.com/warik/gami"

	"github.com/warik/go-dialer/ami"
	"github.com/warik/go-dialer/conf"
	"github.com/warik/go-dialer/db"
	"github.com/warik/go-dialer/model"
	"github.com/warik/go-dialer/s3"
	"github.com/warik/go-dialer/util"
)

func CdrReader(wg *sync.WaitGroup, mChan chan<- db.CDR, finishChan <-chan struct{},
	ticker *time.Ticker) {
	glog.Infoln("Initiating CdrReader...")
	for {
		select {
		case <-finishChan:
			glog.Warningln("Finishing CdrReader...")
			ticker.Stop()
			wg.Done()
			return
		case <-ticker.C:
			cdrs, err := db.GetDB().SelectCDRs(conf.MAX_CDR_NUMBER)
			if err != nil {
				conf.Alert(fmt.Sprintf("Cannot read from cdr | %s", err))
				glog.Errorln(err)
				continue
			}
			for _, cdr := range cdrs {
				mChan <- cdr
			}

			dbCount := db.GetDB().GetCount("cdr")
			glog.Infoln(fmt.Sprintf("<<< READING CDRS | DB: %d | PROCESS: %d", dbCount, len(cdrs)))
			if dbCount >= 2*conf.MAX_CDR_NUMBER {
				conf.Alert(fmt.Sprintf("Overload with cdr, %d", dbCount))
			}

			glog.Flush()
		}
	}
}

func CdrSender(wg *sync.WaitGroup, mChan <-chan db.CDR, finishChan <-chan struct{}, i int) {
	glog.Infoln("Initiating CdrSender...", i)
	for {
		select {
		case <-finishChan:
			glog.Warningln("Finishing CdrSender...", i)
			wg.Done()
			return
		case cdr := <-mChan:
			settings := conf.GetConf().Agencies[cdr.CountryCode]
			url := conf.GetConf().GetApi(cdr.CountryCode, "save_phone_call")
			data, _ := json.Marshal(cdr)
			_, err := util.SendRequest(data, url, "POST", settings.Secret, settings.CompanyId)
			if err == nil {
				glog.Infoln("<<< CDR SENT", "|", cdr.UniqueID)
				res, err := db.GetDB().Delete("cdr", cdr.ID)
				if err != nil {
					glog.Errorln("Error while deleting message - ", cdr.UniqueID, err)
				} else if count, _ := res.RowsAffected(); count != 1 {
					glog.Errorln("CDR was not deleted - ", cdr.UniqueID)
				}
			} else {
				glog.Errorln("<<< ERROR WHILE SENDING", "|", cdr.UniqueID, err)
			}
		}
	}
}

func PhoneCallReader(wg *sync.WaitGroup, pcChan chan<- db.PhoneCall, finishChan <-chan struct{},
	ticker *time.Ticker) {
	glog.Infoln("Initiating PhoneCallReader...")
	for {
		select {
		case <-finishChan:
			glog.Warningln("Finishing PhoneCallReader...")
			ticker.Stop()
			wg.Done()
			return
		case <-ticker.C:
			phoneCalls, err := db.GetDB().SelectPhoneCalls(conf.MAX_PHONE_CALLS_NUMBER)
			if err != nil {
				conf.Alert(fmt.Sprintf("Cannot read from phone_call | %s", err))
				glog.Errorln(err)
				continue
			}
			for _, phoneCall := range phoneCalls {
				pcChan <- phoneCall
			}

			dbCount := db.GetDB().GetCount("phone_call")
			glog.Infoln(fmt.Sprintf("<<< READING PHONE_CALLS | DB: %d | PROCESS: %d", dbCount,
				len(phoneCalls)))
			if dbCount >= 2*conf.MAX_PHONE_CALLS_NUMBER {
				conf.Alert(fmt.Sprintf("Overload with cdr, %d", dbCount))
			}

			glog.Flush()
		}
	}
}

func PhoneCallSender(wg *sync.WaitGroup, pcChan <-chan db.PhoneCall, finishChan <-chan struct{},
	i int) {
	glog.Infoln("Initiating PhoneCallSender...", i)
	dialerName := conf.GetConf().Name
	dirName := conf.GetConf().FolderForCalls
	for {
		select {
		case <-finishChan:
			glog.Warningln("Finishing PhoneCallSender...", i)
			wg.Done()
			return
		case phoneCall := <-pcChan:
			wavFileName := util.GetPhoneCallFileName(dialerName, phoneCall.UniqueID, "wav")
			mp3FileName := util.GetPhoneCallFileName(dialerName, phoneCall.UniqueID, "mp3")

			err := util.ConvertWAV2MP3(dirName, wavFileName, mp3FileName)
			if err != nil {
				glog.Errorln(err)
				continue
			}
			if err = s3.Store(dirName, mp3FileName); err != nil {
				glog.Errorln(err)
				continue
			}
			db.GetDB().Delete("phone_call", phoneCall.ID)
		}
	}
}

func NumbersLoader(wg *sync.WaitGroup, numbersChan chan []string, finishChan <-chan struct{},
	ticker *time.Ticker) {
	glog.Infoln("Initiating NumbersLoader...")
	for {
		select {
		case <-finishChan:
			glog.Warningln("Finishing NumbersLoader...")
			ticker.Stop()
			wg.Done()
			return
		case <-ticker.C:
			go util.LoadInnerNumbers(numbersChan)
		case numbersContainer := <-numbersChan:
			util.InnerPhoneNumbers.Lock()
			glog.Infoln("Processing numbers", numbersContainer)

			countryCode, numbers := numbersContainer[0], numbersContainer[1]
			tNumbersSet := model.Set{}
			// If number is already in the map - lets save also in other set for different handling
			for _, number := range strings.Split(numbers, ",") {
				for country, numbers := range util.InnerPhoneNumbers.NumbersMap {
					if _, ok := numbers[number]; ok && countryCode != country {
						util.InnerPhoneNumbers.DuplicateNumbers[number] = struct{}{}
						break
					}
				}
				tNumbersSet[number] = struct{}{}
			}
			glog.Infoln("Duplicated numbers", util.InnerPhoneNumbers.DuplicateNumbers)

			util.InnerPhoneNumbers.NumbersMap[countryCode] = tNumbersSet
			util.InnerPhoneNumbers.Unlock()
		}
	}
}

func QueueManager(wg *sync.WaitGroup, queueTransport <-chan chan gami.Message, finishChan <-chan struct{},
	ticker *time.Ticker) {
	glog.Infoln("Initiating QueueManager...")
	for {
		select {
		case <-finishChan:
			glog.Warningln("Finishing QueueManager...")
			ticker.Stop()
			wg.Done()
			return
		case <-ticker.C:
			util.InnerPhoneNumbers.RLock()
			glog.Infoln("<<< MANAGING QUEUES...")
			// Send queue status to AMI
			if err := ami.QueueStatus(); err != nil {
				glog.Errorln(err)
				return
			}
			// and wait for channel with active queues from asterisk
			activeQueuesChan := <-queueTransport
			// sort active queues for each number per country
			queuesNumberMap := util.GetActiveQueuesMap(activeQueuesChan)
			for countryCode, settings := range conf.GetConf().Agencies {
				tqs := queuesNumberMap[countryCode]
				numbersState := make(model.Dict)
				// For each inner number get its static queue from asterisk db
				for number, _ := range util.InnerPhoneNumbers.NumbersMap[countryCode] {
					staticQueue, err := ami.GetStaticQueue(number)
					if err != nil {
						// if there is no static queue for number - some problem with it, skip
						continue
					}
					staticQueue = strings.Split(staticQueue, "\n")[0]
					// if there is no active queues for such number, then its not available
					if _, ok := tqs[number]; !ok {
						numbersState[number] = "not_available"
					} else {
						// if there are some, which are not its static queue and not general queue
						// (same as static but without last digit)
						// then number should be removed from them and still not available
						status := "not_available"
						for _, queue := range tqs[number] {
							generalizedQueue := staticQueue[:len(staticQueue)-1]
							if staticQueue == queue || generalizedQueue == queue {
								status = "available"
							} else {
								//_, err := ami.RemoveFromQueue(queue, countryCode, number)
								//	if err != nil {
								//	glog.Errorln(err, number)
								//}
							}
						}
						numbersState[number] = status
					}
				}
				glog.Infoln("NUMBERS STATE", numbersState)

				url := conf.GetConf().GetApi(countryCode, "save_company_queues_states")
				payload, _ := json.Marshal(numbersState)
				_, err := util.SendRequest(payload, url, "POST", settings.Secret,
					settings.CompanyId)
				if err != nil {
					glog.Errorln(err, url)
				}
			}
			util.InnerPhoneNumbers.RUnlock()
		}
	}
}
